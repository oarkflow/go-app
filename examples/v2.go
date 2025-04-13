package main

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"gopkg.in/yaml.v2"
)

type Config struct {
	MailboxSize         int           `yaml:"mailbox_size"`
	MaxRestarts         int           `yaml:"max_restarts"`
	BackoffBase         time.Duration `yaml:"backoff_base"`
	RateLimitPerSecond  int           `yaml:"rate_limit_per_second"`
	HealthCheckEndpoint string        `yaml:"health_check_endpoint"`
	RemoteEndpoint      string        `yaml:"remote_endpoint"`
	HTTPPort            string        `yaml:"http_port"`
}

func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.MailboxSize == 0 {
		cfg.MailboxSize = 1024
	}
	if cfg.MaxRestarts == 0 {
		cfg.MaxRestarts = 5
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = 1 * time.Millisecond
	}
	if cfg.RateLimitPerSecond == 0 {
		cfg.RateLimitPerSecond = 1000000
	}
	if cfg.HealthCheckEndpoint == "" {
		cfg.HealthCheckEndpoint = ":9090"
	}
	if cfg.RemoteEndpoint == "" {
		cfg.RemoteEndpoint = "http://localhost:8080/remote"
	}
	if cfg.HTTPPort == "" {
		cfg.HTTPPort = ":3000"
	}
	return &cfg, nil
}

type Message any
type ResponseMessage struct {
	Data any
	Err  error
}

type Actor interface {
	PreStart(ctx ActorContext)
	Receive(ctx ActorContext, message Message)
	PostStop(ctx ActorContext)
	PreRestart(ctx ActorContext, reason error)
}

type Behavior func(ctx ActorContext, msg Message)

type BaseActor struct {
	mu            sync.Mutex
	behavior      Behavior
	behaviorStack []Behavior
}

func (b *BaseActor) Become(newBehavior Behavior) {
	b.mu.Lock()
	if b.behavior != nil {
		b.behaviorStack = append(b.behaviorStack, b.behavior)
	}
	b.behavior = newBehavior
	b.mu.Unlock()
}

func (b *BaseActor) Unbecome() {
	b.mu.Lock()
	if len(b.behaviorStack) > 0 {
		b.behavior = b.behaviorStack[len(b.behaviorStack)-1]
		b.behaviorStack = b.behaviorStack[:len(b.behaviorStack)-1]
	}
	b.mu.Unlock()
}

func (b *BaseActor) DefaultReceive(ctx ActorContext, msg Message) {
	b.mu.Lock()
	beh := b.behavior
	b.mu.Unlock()
	if beh != nil {
		beh(ctx, msg)
	}
}

type Persistence interface {
	SaveSnapshot(actorID string, state any) error
	RestoreSnapshot(actorID string) (any, error)
}

type DummyPersistence struct {
	snapshots sync.Map
}

func (p *DummyPersistence) SaveSnapshot(actorID string, state any) error {
	p.snapshots.Store(actorID, state)
	structuredLog("persistence", "Snapshot saved", map[string]any{"actor": actorID})
	return nil
}

func (p *DummyPersistence) RestoreSnapshot(actorID string) (any, error) {
	if state, ok := p.snapshots.Load(actorID); ok {
		structuredLog("persistence", "Snapshot restored", map[string]any{"actor": actorID})
		return state, nil
	}
	return nil, errors.New("no snapshot found")
}

var persistenceManager Persistence = &DummyPersistence{}

type EventLogger interface {
	LogEvent(actorID string, event any) error
	GetEvents(actorID string) ([]any, error)
}

type DummyEventLogger struct {
	events sync.Map
}

func (l *DummyEventLogger) LogEvent(actorID string, event any) error {
	val, _ := l.events.LoadOrStore(actorID, []any{})
	events := val.([]any)
	events = append(events, event)
	l.events.Store(actorID, events)
	structuredLog("event", "Event logged", map[string]any{"actor": actorID, "event": event})
	return nil
}

func (l *DummyEventLogger) GetEvents(actorID string) ([]any, error) {
	if events, ok := l.events.Load(actorID); ok {
		return events.([]any), nil
	}
	return nil, errors.New("no events found")
}

var eventLogger EventLogger = &DummyEventLogger{}

type PubSub struct {
	mu          sync.RWMutex
	subscribers map[string][]*ActorRef
}

func NewPubSub() *PubSub {
	return &PubSub{subscribers: make(map[string][]*ActorRef)}
}

func (ps *PubSub) Subscribe(topic string, ref *ActorRef) {
	ps.mu.Lock()
	ps.subscribers[topic] = append(ps.subscribers[topic], ref)
	ps.mu.Unlock()
}

func (ps *PubSub) Publish(topic string, msg Message) {
	ps.mu.RLock()
	refs, exists := ps.subscribers[topic]
	ps.mu.RUnlock()
	if !exists {
		return
	}
	for _, ref := range refs {
		if err := ref.Tell(msg); err != nil {
			structuredLog("pubsub", "Delivery failed", map[string]any{"actor": ref.pid, "error": err.Error()})
		}
	}
}

var globalPubSub = NewPubSub()

type MessageInterceptor func(next func(ctx ActorContext, msg Message)) func(ctx ActorContext, msg Message)

func ChainInterceptors(base func(ctx ActorContext, msg Message), interceptors ...MessageInterceptor) func(ctx ActorContext, msg Message) {
	chain := base
	for i := len(interceptors) - 1; i >= 0; i-- {
		chain = interceptors[i](chain)
	}
	return chain
}

type Metrics interface {
	Increment(metric string)
	Observe(metric string, value float64)
}

type DummyMetrics struct{}

func (m *DummyMetrics) Increment(metric string) {
	structuredLog("metrics", "Incremented", map[string]any{"metric": metric})
}

func (m *DummyMetrics) Observe(metric string, value float64) {
	structuredLog("metrics", "Observed", map[string]any{"metric": metric, "value": value})
}

var metrics Metrics = &DummyMetrics{}

func LoggingInterceptor(next func(ctx ActorContext, msg Message)) func(ctx ActorContext, msg Message) {
	return func(ctx ActorContext, msg Message) {
		structuredLog("interceptor", "Processing message", map[string]any{"actor": ctx.Self.pid, "message": msg})
		metrics.Increment("messages.processed")
		next(ctx, msg)
	}
}

func RateLimitingInterceptor(maxPerSec int) MessageInterceptor {
	var mu sync.Mutex
	var count int
	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for range ticker.C {
			mu.Lock()
			count = 0
			mu.Unlock()
		}
	}()
	return func(next func(ctx ActorContext, msg Message)) func(ctx ActorContext, msg Message) {
		return func(ctx ActorContext, msg Message) {
			mu.Lock()
			if count >= maxPerSec {
				mu.Unlock()
				structuredLog("interceptor", "Rate limit exceeded", map[string]any{"actor": ctx.Self.pid, "message": msg})
				metrics.Increment("messages.dropped")
				return
			}
			count++
			mu.Unlock()
			next(ctx, msg)
		}
	}
}

var deadLetterQueue = make(chan Message, 10000)

func startDeadLetterProcessor() {
	go func() {
		for msg := range deadLetterQueue {
			structuredLog("deadletter", "Processing dead-letter message", map[string]any{"message": msg})
		}
	}()
}

func enqueueDeadLetter(msg Message) {
	select {
	case deadLetterQueue <- msg:
	default:
		// In worst-case, drop silently if DLQ is full.
	}
}

func structuredLog(component, msg string, fields map[string]any) {
	entry, _ := json.Marshal(fields)
	log.Printf("[%s] %s - %s", component, msg, string(entry))
}

type ActorRef struct {
	pid     string
	mailbox Mailbox
	system  *ActorSystem
	running atomic.Bool
}

func (r *ActorRef) Tell(message Message) error {
	if !r.running.Load() {
		return errors.New("actor not running")
	}
	err := r.mailbox.Enqueue(message)
	if err != nil {
		enqueueDeadLetter(map[string]any{"actor": r.pid, "message": message})
		structuredLog("deadletter", "Dropped message", map[string]any{"actor": r.pid})
	}
	return err
}

func (r *ActorRef) Ask(message Message, timeout time.Duration) (ResponseMessage, error) {
	replyChan := make(chan ResponseMessage, 1)
	envelope := Envelope{Message: message, ReplyChan: replyChan}
	if err := r.mailbox.Enqueue(envelope); err != nil {
		return ResponseMessage{}, err
	}
	select {
	case resp := <-replyChan:
		return resp, nil
	case <-time.After(timeout):
		return ResponseMessage{}, errors.New("timeout waiting for response")
	}
}

type Envelope struct {
	Message   Message
	ReplyChan chan ResponseMessage
}

type Mailbox interface {
	Enqueue(message Message) error
	Dequeue() (Message, bool)
	Close()
}

type StandardMailbox struct {
	ch chan Message
}

func NewStandardMailbox(capacity int) *StandardMailbox {
	return &StandardMailbox{ch: make(chan Message, capacity)}
}

func (m *StandardMailbox) Enqueue(message Message) error {
	for i := 0; i < 3; i++ {
		select {
		case m.ch <- message:
			return nil
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	return errors.New("mailbox full after retries")
}

func (m *StandardMailbox) Dequeue() (Message, bool) {
	msg, ok := <-m.ch
	return msg, ok
}

func (m *StandardMailbox) Close() {
	close(m.ch)
}

type PriorityMessage struct {
	priority int
	message  Message
	index    int
}

type PriorityMailbox struct {
	mu       sync.Mutex
	cond     *sync.Cond
	messages priorityQueue
	closed   bool
}

func NewPriorityMailbox() *PriorityMailbox {
	pm := &PriorityMailbox{messages: make(priorityQueue, 0)}
	pm.cond = sync.NewCond(&pm.mu)
	return pm
}

func (pm *PriorityMailbox) Enqueue(message Message) error {
	var p int
	if pe, ok := message.(PriorityEnvelope); ok {
		p = pe.Priority
		message = pe.Envelope
	} else {
		p = 1000
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.closed {
		return errors.New("mailbox closed")
	}
	heap.Push(&pm.messages, &PriorityMessage{priority: p, message: message})
	pm.cond.Signal()
	return nil
}

func (pm *PriorityMailbox) Dequeue() (Message, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for len(pm.messages) == 0 && !pm.closed {
		pm.cond.Wait()
	}
	if len(pm.messages) == 0 {
		return nil, false
	}
	pmsg := heap.Pop(&pm.messages).(*PriorityMessage)
	return pmsg.message, true
}

func (pm *PriorityMailbox) Close() {
	pm.mu.Lock()
	pm.closed = true
	pm.cond.Broadcast()
	pm.mu.Unlock()
}

type PriorityEnvelope struct {
	Priority int
	Envelope Envelope
}

type priorityQueue []*PriorityMessage

func (pq priorityQueue) Len() int           { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool { return pq[i].priority < pq[j].priority }
func (pq priorityQueue) Swap(i, j int)      { pq[i], pq[j] = pq[j], pq[i]; pq[i].index = i; pq[j].index = j }
func (pq *priorityQueue) Push(x any) {
	n := len(*pq)
	item := x.(*PriorityMessage)
	item.index = n
	*pq = append(*pq, item)
}
func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	item.index = -1
	*pq = old[0 : n-1]
	return item
}

type actorCell struct {
	ref         ActorRef
	actor       Actor
	mailbox     Mailbox
	props       ActorProps
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	parent      *actorCell
	children    sync.Map
	supervisor  SupervisionStrategy
	interceptor func(ctx ActorContext, msg Message)
	mu          sync.Mutex
}

func (cell *actorCell) run() {
	cell.wg.Add(1)
	go func() {
		defer cell.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("actor %s panicked: %v", cell.ref.pid, r)
				structuredLog("actor_system", "Panic recovered", map[string]any{"actor": cell.ref.pid, "error": err.Error()})
				if cell.parent != nil && cell.parent.supervisor != nil {
					cell.parent.supervisor.HandleFailure(cell, err)
				}
			}
		}()
		actCtx, cancel := context.WithCancel(cell.ctx)
		cell.cancel = cancel
		actorContext := ActorContext{
			Self:       cell.ref,
			Parent:     nil,
			Context:    actCtx,
			CancelFunc: cancel,
		}
		cell.actor.PreStart(actorContext)
		for {
			select {
			case <-cell.ctx.Done():
				cell.actor.PostStop(actorContext)
				return
			default:
				msg, ok := cell.mailbox.Dequeue()
				if !ok {
					cell.actor.PostStop(actorContext)
					return
				}
				if cell.interceptor != nil {
					cell.interceptor(actorContext, msg)
				} else {
					switch env := msg.(type) {
					case Envelope:
						cell.actor.Receive(actorContext, env)
					default:
						cell.actor.Receive(actorContext, msg)
					}
				}
			}
		}
	}()
}

func (cell *actorCell) stop() {
	cell.cancel()
	cell.mailbox.Close()
	cell.wg.Wait()
}

type SupervisionStrategy interface {
	HandleFailure(child *actorCell, reason error)
}

type OneForOneStrategy struct {
	maxRestarts int
	backoff     time.Duration
	restarts    sync.Map
}

func (s *OneForOneStrategy) HandleFailure(child *actorCell, reason error) {
	actorID := child.ref.pid
	countAny, _ := s.restarts.LoadOrStore(actorID, 0)
	count := countAny.(int)
	if count >= s.maxRestarts {
		structuredLog("supervision", "Max restarts exceeded", map[string]any{"actor": actorID})
		return
	}
	structuredLog("supervision", "Restarting actor", map[string]any{"actor": actorID, "attempt": count + 1})
	delay := time.Duration(math.Pow(2, float64(count))) * s.backoff
	time.Sleep(delay)
	s.restarts.Store(actorID, count+1)
	actorCtx := ActorContext{
		Self:    child.ref,
		Context: child.ctx,
	}
	child.actor.PreRestart(actorCtx, reason)
	child.mu.Lock()
	if child.props.MailboxType == MailboxPriority {
		child.mailbox = NewPriorityMailbox()
	} else {
		child.mailbox = NewStandardMailbox(child.props.MailboxSize)
	}
	child.ref.mailbox = child.mailbox
	child.mu.Unlock()
	child.run()
}

type ActorProps struct {
	MailboxSize  int
	MailboxType  MailboxType
	Interceptors []MessageInterceptor
	Remote       bool
}

type MailboxType int

const (
	MailboxStandard MailboxType = iota
	MailboxPriority
)

type RemoteTransport interface {
	SendRemote(actorID string, msg Message) error
}

type JSONRemoteTransport struct {
	endpoint string
	client   *http.Client
}

func (t *JSONRemoteTransport) SendRemote(actorID string, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("serialization error: %w", err)
	}
	structuredLog("remote", "Sending remote message", map[string]any{"actor": actorID, "data": string(data)})
	http.Post(t.endpoint, "application/json", nil)
	return nil
}

type ActorSystem struct {
	registry        sync.Map
	ctx             context.Context
	cancel          context.CancelFunc
	supervisor      SupervisionStrategy
	remoteTransport RemoteTransport
	config          *Config
	activeActors    atomic.Int32
}

func NewActorSystem(cfg *Config) *ActorSystem {
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := &OneForOneStrategy{maxRestarts: cfg.MaxRestarts, backoff: cfg.BackoffBase}
	startTimeCtx := context.WithValue(ctx, "startTime", time.Now())
	return &ActorSystem{
		ctx:        startTimeCtx,
		cancel:     cancel,
		supervisor: supervisor,
		remoteTransport: &JSONRemoteTransport{
			endpoint: cfg.RemoteEndpoint,
			client:   &http.Client{Timeout: 10 * time.Second},
		},
		config: cfg,
	}
}

func (sys *ActorSystem) Spawn(pid string, actor Actor, props ActorProps) *ActorRef {
	actorCtx, cancel := context.WithCancel(sys.ctx)
	_ = cancel
	var mbox Mailbox
	if props.MailboxType == MailboxPriority {
		mbox = NewPriorityMailbox()
	} else {
		capacity := props.MailboxSize
		if capacity <= 0 {
			capacity = sys.config.MailboxSize
		}
		mbox = NewStandardMailbox(capacity)
	}
	ref := ActorRef{pid: pid, mailbox: mbox, system: sys}
	cell := &actorCell{ref: ref, actor: actor, mailbox: mbox, props: props, ctx: actorCtx, supervisor: sys.supervisor}
	if len(props.Interceptors) > 0 {
		cell.interceptor = ChainInterceptors(func(ctx ActorContext, msg Message) {
			switch env := msg.(type) {
			case Envelope:
				actor.Receive(ctx, env)
			default:
				actor.Receive(ctx, msg)
			}
		}, props.Interceptors...)
	}
	ref.running.Store(true)
	sys.registry.Store(pid, cell)
	sys.activeActors.Add(1)
	cell.run()
	return &ref
}

func (sys *ActorSystem) Stop(pid string) error {
	value, ok := sys.registry.Load(pid)
	if !ok {
		return fmt.Errorf("actor %s not found", pid)
	}
	cell := value.(*actorCell)
	cell.stop()
	sys.registry.Delete(pid)
	sys.activeActors.Add(-1)
	return nil
}

func (sys *ActorSystem) Shutdown() {
	sys.registry.Range(func(key, value any) bool {
		cell := value.(*actorCell)
		cell.stop()
		sys.registry.Delete(key)
		structuredLog("shutdown", "Actor stopped", map[string]any{"actor": cell.ref.pid})
		sys.activeActors.Add(-1)
		return true
	})
	sys.cancel()
	time.Sleep(500 * time.Millisecond)
	structuredLog("shutdown", "Actor system shutdown complete", nil)
}

func (sys *ActorSystem) ServeHealthCheck(addr string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		status := map[string]any{
			"active_actors": sys.activeActors.Load(),
			"uptime":        time.Since(sys.ctx.Value("startTime").(time.Time)).String(),
		}
		data, _ := json.Marshal(status)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})
	go func() {
		structuredLog("health", "Starting health endpoint", map[string]any{"address": addr})
		if err := http.ListenAndServe(addr, nil); err != nil {
			structuredLog("health", "Endpoint error", map[string]any{"error": err.Error()})
		}
	}()
}

type Router struct {
	actors  []*ActorRef
	counter uint64
}

func NewRoundRobinRouter(actors []*ActorRef) *Router {
	return &Router{actors: actors}
}

func (r *Router) Route(message Message) {
	idx := atomic.AddUint64(&r.counter, 1)
	target := r.actors[int(idx)%len(r.actors)]
	if err := target.Tell(message); err != nil {
		structuredLog("router", "Error sending", map[string]any{"error": err.Error()})
	}
}

type ConsistentHashRouter struct {
	actors []*ActorRef
}

func NewConsistentHashRouter(actors []*ActorRef) *ConsistentHashRouter {
	return &ConsistentHashRouter{actors: actors}
}

func (r *ConsistentHashRouter) RouteByKey(message Message, key string) {
	hash := 0
	for _, b := range key {
		hash += int(b)
	}
	index := hash % len(r.actors)
	if err := r.actors[index].Tell(message); err != nil {
		structuredLog("router", "Error sending", map[string]any{"error": err.Error()})
	}
}

type TypedMessage[T any] struct {
	Payload T
}

func NewTypedMessage[T any](payload T) TypedMessage[T] {
	return TypedMessage[T]{Payload: payload}
}

type ActorContext struct {
	Self       ActorRef
	Parent     *ActorRef
	Context    context.Context
	CancelFunc context.CancelFunc
}

type RequestMessage struct {
	ID      string
	Payload string
	Reply   chan ResponseMessage
}

type RequestHandlerActor struct{}

func (a *RequestHandlerActor) PreStart(ctx ActorContext) {
	structuredLog("actor", "RequestHandlerActor starting", nil)
}

func (a *RequestHandlerActor) Receive(ctx ActorContext, msg Message) {
	switch req := msg.(type) {
	case RequestMessage:
		response := fmt.Sprintf("Processed request %s with payload: %s", req.ID, req.Payload)
		req.Reply <- ResponseMessage{Data: response}
	default:
		structuredLog("actor", "RequestHandlerActor unknown message", map[string]any{"message": msg})
	}
}

func (a *RequestHandlerActor) PostStop(ctx ActorContext) {
	structuredLog("actor", "RequestHandlerActor stopping", nil)
}

func (a *RequestHandlerActor) PreRestart(ctx ActorContext, reason error) {
	structuredLog("actor", "RequestHandlerActor restarting", map[string]any{"reason": reason.Error()})
}

type EchoActor struct {
	name string
	BaseActor
}

func (a *EchoActor) PreStart(ctx ActorContext) {
	structuredLog("actor", "EchoActor starting", map[string]any{"actor": a.name})
	a.Become(func(ctx ActorContext, msg Message) {
		switch env := msg.(type) {
		case Envelope:
			structuredLog("actor", "EchoActor received envelope", map[string]any{"actor": a.name, "message": env.Message})
			if env.ReplyChan != nil {
				respData, _ := json.Marshal(fmt.Sprintf("Echo: %v", env.Message))
				env.ReplyChan <- ResponseMessage{Data: string(respData)}
			}
		default:
			structuredLog("actor", "EchoActor received message", map[string]any{"actor": a.name, "message": msg})
		}
	})
}

func (a *EchoActor) Receive(ctx ActorContext, msg Message) {
	a.DefaultReceive(ctx, msg)
}

func (a *EchoActor) PostStop(ctx ActorContext) {
	structuredLog("actor", "EchoActor stopping", map[string]any{"actor": a.name})
}

func (a *EchoActor) PreRestart(ctx ActorContext, reason error) {
	structuredLog("actor", "EchoActor restarting", map[string]any{"actor": a.name, "reason": reason.Error()})
}

type TimerActor struct {
	name string
}

func (a *TimerActor) PreStart(ctx ActorContext) {
	structuredLog("actor", "TimerActor starting", map[string]any{"actor": a.name})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Context.Done():
				return
			case <-ticker.C:
				if err := ctx.Self.Tell("tick"); err != nil {
					structuredLog("actor", "TimerActor error sending tick", map[string]any{"actor": a.name, "error": err.Error()})
				}
			}
		}
	}()
}

func (a *TimerActor) Receive(ctx ActorContext, msg Message) {
	structuredLog("actor", "TimerActor received message", map[string]any{"actor": a.name, "message": msg})
}

func (a *TimerActor) PostStop(ctx ActorContext) {
	structuredLog("actor", "TimerActor stopping", map[string]any{"actor": a.name})
}

func (a *TimerActor) PreRestart(ctx ActorContext, reason error) {
	structuredLog("actor", "TimerActor restarting", map[string]any{"actor": a.name, "reason": reason.Error()})
}

type PersistentActor struct {
	name  string
	state map[string]any
	BaseActor
}

func (a *PersistentActor) PreStart(ctx ActorContext) {
	structuredLog("actor", "PersistentActor starting", map[string]any{"actor": a.name})
	if restored, err := persistenceManager.RestoreSnapshot(a.name); err == nil {
		a.state = restored.(map[string]any)
	} else {
		a.state = make(map[string]any)
	}
	a.Become(func(ctx ActorContext, msg Message) {
		structuredLog("actor", "PersistentActor processing", map[string]any{"actor": a.name, "message": msg})
		a.state["lastMessage"] = msg
		_ = persistenceManager.SaveSnapshot(a.name, a.state)
		_ = eventLogger.LogEvent(a.name, msg)
	})
}

func (a *PersistentActor) Receive(ctx ActorContext, msg Message) {
	a.DefaultReceive(ctx, msg)
}

func (a *PersistentActor) PostStop(ctx ActorContext) {
	structuredLog("actor", "PersistentActor stopping", map[string]any{"actor": a.name})
}

func (a *PersistentActor) PreRestart(ctx ActorContext, reason error) {
	structuredLog("actor", "PersistentActor restarting", map[string]any{"actor": a.name, "reason": reason.Error()})
}

func main() {
	startedAt := time.Now()
	cfg, err := LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	ctx := context.WithValue(context.Background(), "startTime", startedAt)
	startDeadLetterProcessor()
	system := NewActorSystem(cfg)
	system.ctx = ctx
	system.ServeHealthCheck(cfg.HealthCheckEndpoint)
	interceptors := []MessageInterceptor{
		LoggingInterceptor,
		RateLimitingInterceptor(cfg.RateLimitPerSecond),
	}
	echoProps := ActorProps{
		MailboxSize:  cfg.MailboxSize,
		MailboxType:  MailboxStandard,
		Interceptors: interceptors,
	}
	echo := &EchoActor{name: "Echo1"}
	echoRef := system.Spawn("echo1", echo, echoProps)
	var poolRefs []*ActorRef
	for i := 2; i <= 4; i++ {
		a := &EchoActor{name: fmt.Sprintf("Echo%d", i)}
		ref := system.Spawn(fmt.Sprintf("echo%d", i), a, echoProps)
		poolRefs = append(poolRefs, ref)
	}
	router := NewRoundRobinRouter(poolRefs)
	app := fiber.New()
	reqHandler := &RequestHandlerActor{}
	reqHandlerRef := system.Spawn("reqHandler", reqHandler, ActorProps{
		MailboxSize:  1024,
		MailboxType:  MailboxStandard,
		Interceptors: interceptors,
	})
	app.Post("/process", func(c *fiber.Ctx) error {
		var payload map[string]any
		if err := c.BodyParser(&payload); err != nil {
			return c.Status(fiber.StatusBadRequest).SendString(err.Error())
		}
		reqID := fmt.Sprintf("%d", time.Now().UnixNano())
		replyChan := make(chan ResponseMessage, 1)
		reqMsg := RequestMessage{
			ID:      reqID,
			Payload: payload["data"].(string),
			Reply:   replyChan,
		}
		if err := reqHandlerRef.Tell(reqMsg); err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("Actor error")
		}
		select {
		case resp := <-replyChan:
			return c.JSON(map[string]any{"response": resp.Data})
		case <-time.After(3 * time.Second):
			return c.Status(fiber.StatusGatewayTimeout).SendString("Timeout")
		}
	})
	app.Get("/benchmark", func(c *fiber.Ctx) error {
		const iterations = 10_000_000
		start := time.Now()
		for i := 0; i < iterations; i++ {
			_ = echoRef.Tell(fmt.Sprintf("msg %d", i))
		}
		duration := time.Since(start)
		return c.JSON(map[string]any{"messages": iterations, "duration_ms": duration.Milliseconds()})
	})
	app.Get("/metrics", func(c *fiber.Ctx) error {
		metricsData := map[string]any{
			"active_actors": system.activeActors.Load(),
			"start_time":    system.ctx.Value("startTime"),
		}
		return c.JSON(metricsData)
	})
	go func() {
		if err := app.Listen(cfg.HTTPPort); err != nil {
			log.Fatalf("Fiber server error: %v", err)
		}
	}()
	for i := 0; i < 5; i++ {
		router.Route(fmt.Sprintf("Route Message #%d", i+1))
	}
	timerActor := &TimerActor{name: "Timer1"}
	system.Spawn("timer1", timerActor, ActorProps{
		MailboxSize:  cfg.MailboxSize,
		MailboxType:  MailboxStandard,
		Interceptors: interceptors,
	})
	persistentActor := &PersistentActor{name: "Persist1"}
	system.Spawn("persist1", persistentActor, ActorProps{
		MailboxSize:  cfg.MailboxSize + 5,
		MailboxType:  MailboxStandard,
		Interceptors: interceptors,
	})
	globalPubSub.Subscribe("news", echoRef)
	globalPubSub.Publish("news", "Breaking: New Actor Framework Released!")
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan
	log.Println("Shutdown signal received, shutting down system...")
	if err := app.Shutdown(); err != nil {
		log.Printf("Fiber shutdown error: %v", err)
	}
	system.Shutdown()
}
