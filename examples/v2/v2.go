// main.go
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
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"
	"gopkg.in/yaml.v3" // go get gopkg.in/yaml.v2
)

////////////////////////////////////////////////////////////////////////////////
// Section 0. Configuration Management
////////////////////////////////////////////////////////////////////////////////

// Config holds runtime configuration parameters.
type Config struct {
	MailboxSize         int           `yaml:"mailbox_size"`
	MaxRestarts         int           `yaml:"max_restarts"`
	BackoffBase         time.Duration `yaml:"backoff_base"`
	RateLimitPerSecond  int           `yaml:"rate_limit_per_second"`
	HealthCheckEndpoint string        `yaml:"health_check_endpoint"`
	RemoteEndpoint      string        `yaml:"remote_endpoint"`
}

// LoadConfig loads configuration from a YAML file.
func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	// Set defaults if not provided.
	if cfg.MailboxSize == 0 {
		cfg.MailboxSize = 10
	}
	if cfg.MaxRestarts == 0 {
		cfg.MaxRestarts = 5
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = 1 * time.Second
	}
	if cfg.RateLimitPerSecond == 0 {
		cfg.RateLimitPerSecond = 10
	}
	if cfg.HealthCheckEndpoint == "" {
		cfg.HealthCheckEndpoint = ":9090"
	}
	if cfg.RemoteEndpoint == "" {
		cfg.RemoteEndpoint = "http://localhost:8080/remote"
	}
	return &cfg, nil
}

////////////////////////////////////////////////////////////////////////////////
// Section 1. Actor Interface, Dynamic Behavior & BaseActor
////////////////////////////////////////////////////////////////////////////////

type Message interface{}

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

// Behavior is a function type that describes an actor’s message processing.
type Behavior func(ctx ActorContext, msg Message)

// BaseActor provides a default implementation for dynamic behavior switching.
type BaseActor struct {
	mu            sync.Mutex
	behavior      Behavior
	behaviorStack []Behavior
}

func (b *BaseActor) Become(newBehavior Behavior) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.behavior != nil {
		b.behaviorStack = append(b.behaviorStack, b.behavior)
	}
	b.behavior = newBehavior
}

func (b *BaseActor) Unbecome() {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(b.behaviorStack)
	if n == 0 {
		return
	}
	b.behavior = b.behaviorStack[n-1]
	b.behaviorStack = b.behaviorStack[:n-1]
}

func (b *BaseActor) DefaultReceive(ctx ActorContext, msg Message) {
	b.mu.Lock()
	beh := b.behavior
	b.mu.Unlock()
	if beh != nil {
		beh(ctx, msg)
	}
}

////////////////////////////////////////////////////////////////////////////////
// Section 2. Persistence & Event Sourcing (Stubs for Production)
////////////////////////////////////////////////////////////////////////////////

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

////////////////////////////////////////////////////////////////////////////////
// Section 3. Pub/Sub Mechanism
////////////////////////////////////////////////////////////////////////////////

type PubSub struct {
	mu          sync.RWMutex
	subscribers map[string][]*ActorRef
}

func NewPubSub() *PubSub {
	return &PubSub{
		subscribers: make(map[string][]*ActorRef),
	}
}

func (ps *PubSub) Subscribe(topic string, ref *ActorRef) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.subscribers[topic] = append(ps.subscribers[topic], ref)
}

func (ps *PubSub) Publish(topic string, msg Message) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	refs, exists := ps.subscribers[topic]
	if !exists {
		return
	}
	for _, ref := range refs {
		if err := ref.Tell(msg); err != nil {
			structuredLog("pubsub", "Message delivery failed", map[string]any{"actor": ref.pid, "error": err.Error()})
		}
	}
}

var globalPubSub = NewPubSub()

////////////////////////////////////////////////////////////////////////////////
// Section 4. Middleware / Interceptors & Metrics
////////////////////////////////////////////////////////////////////////////////

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

// LoggingInterceptor provides structured logging for each message.
func LoggingInterceptor(next func(ctx ActorContext, msg Message)) func(ctx ActorContext, msg Message) {
	return func(ctx ActorContext, msg Message) {
		structuredLog("interceptor", "Message processing", map[string]any{"actor": ctx.Self.pid, "message": msg})
		metrics.Increment("messages.processed")
		next(ctx, msg)
	}
}

// RateLimitingInterceptor allows at most maxPerSec messages per actor per second.
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
				// Optionally push to a dead-letter queue here.
				return
			}
			count++
			mu.Unlock()
			next(ctx, msg)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// Section 5. Structured Logging Helper
////////////////////////////////////////////////////////////////////////////////

// structuredLog wraps standard logging with key/value context.
func structuredLog(component, msg string, fields map[string]any) {
	entry, _ := json.Marshal(fields)
	log.Printf("[%s] %s - %s", component, msg, string(entry))
}

////////////////////////////////////////////////////////////////////////////////
// Section 6. Actor Addressing and Mailbox Implementations
////////////////////////////////////////////////////////////////////////////////

type ActorRef struct {
	pid     string
	mailbox Mailbox
	system  *ActorSystem
	running atomic.Bool
}

func (r *ActorRef) Tell(message Message) error {
	if !r.running.Load() {
		return errors.New("actor is not running")
	}
	err := r.mailbox.Enqueue(message)
	if err != nil {
		// Push to dead-letter queue (here we just log it)
		structuredLog("deadletter", "Message dropped", map[string]any{"actor": r.pid, "message": message})
	}
	return err
}

func (r *ActorRef) Ask(message Message, timeout time.Duration) (ResponseMessage, error) {
	replyChan := make(chan ResponseMessage, 1)
	envelope := Envelope{
		Message:   message,
		ReplyChan: replyChan,
	}
	err := r.mailbox.Enqueue(envelope)
	if err != nil {
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

// Mailbox abstracts a message queue.
type Mailbox interface {
	Enqueue(message Message) error
	Dequeue() (Message, bool)
	Close()
}

// StandardMailbox implements a buffered mailbox.
type StandardMailbox struct {
	ch chan Message
}

func NewStandardMailbox(capacity int) *StandardMailbox {
	return &StandardMailbox{ch: make(chan Message, capacity)}
}

func (m *StandardMailbox) Enqueue(message Message) error {
	// Try multiple times before giving up.
	for i := 0; i < 3; i++ {
		select {
		case m.ch <- message:
			return nil
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	return errors.New("mailbox full after retries, message dropped")
}

func (m *StandardMailbox) Dequeue() (Message, bool) {
	msg, ok := <-m.ch
	return msg, ok
}

func (m *StandardMailbox) Close() {
	close(m.ch)
}

// PriorityMailbox uses a heap for prioritized messages.
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
	pm := &PriorityMailbox{
		messages: make(priorityQueue, 0),
	}
	pm.cond = sync.NewCond(&pm.mu)
	return pm
}

func (pm *PriorityMailbox) Enqueue(message Message) error {
	var p int
	if pe, ok := message.(PriorityEnvelope); ok {
		p = pe.Priority
		message = pe.Envelope
	} else {
		p = 1000 // default low priority
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.closed {
		return errors.New("mailbox is closed")
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

func (pq priorityQueue) Len() int { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool {
	return pq[i].priority < pq[j].priority
}
func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}
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

////////////////////////////////////////////////////////////////////////////////
// Section 7. Actor Cell & Execution with Middleware Support
////////////////////////////////////////////////////////////////////////////////

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
		// Recover from panics.
		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("actor %s panicked: %v", cell.ref.pid, r)
				structuredLog("actor_system", "panic recovered", map[string]any{"actor": cell.ref.pid, "error": err.Error()})
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

////////////////////////////////////////////////////////////////////////////////
// Section 8. Supervision & Advanced Restart Strategy
////////////////////////////////////////////////////////////////////////////////

type SupervisionStrategy interface {
	HandleFailure(child *actorCell, reason error)
}

type OneForOneStrategy struct {
	maxRestarts int
	backoff     time.Duration
	restarts    sync.Map // map[string]int
}

func (s *OneForOneStrategy) HandleFailure(child *actorCell, reason error) {
	actorID := child.ref.pid
	countAny, _ := s.restarts.LoadOrStore(actorID, 0)
	count := countAny.(int)
	if count >= s.maxRestarts {
		structuredLog("supervision", "Actor exceeded max restarts", map[string]any{"actor": actorID})
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

////////////////////////////////////////////////////////////////////////////////
// Section 9. Actor System, Registry & Remote Transport Stub
////////////////////////////////////////////////////////////////////////////////

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

// RemoteTransport is a stub interface for remote messaging.
type RemoteTransport interface {
	SendRemote(actorID string, msg Message) error
}

// JSONRemoteTransport simulates remote messaging using JSON over HTTP.
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
	// In production, perform an HTTP POST.
	http.Post(t.endpoint, "application/json", nil)
	return nil
}

type ActorSystem struct {
	registry        sync.Map // map[string]*actorCell
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
	return &ActorSystem{
		ctx:             ctx,
		cancel:          cancel,
		supervisor:      supervisor,
		remoteTransport: &JSONRemoteTransport{endpoint: cfg.RemoteEndpoint, client: &http.Client{}},
		config:          cfg,
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
	ref := ActorRef{
		pid:     pid,
		mailbox: mbox,
		system:  sys,
	}
	cell := &actorCell{
		ref:        ref,
		actor:      actor,
		mailbox:    mbox,
		props:      props,
		ctx:        actorCtx,
		supervisor: sys.supervisor,
	}
	// Compose interceptor chain if interceptors provided.
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
		return fmt.Errorf("actor with pid %s not found", pid)
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

////////////////////////////////////////////////////////////////////////////////
// Section 10. Router Implementations (Round-Robin & Consistent Hashing Stub)
////////////////////////////////////////////////////////////////////////////////

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
	err := target.Tell(message)
	if err != nil {
		structuredLog("router", "Error sending message", map[string]any{"error": err.Error()})
	}
}

// ConsistentHashRouter stub for future extension.
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
		structuredLog("router", "Error sending message", map[string]any{"error": err.Error()})
	}
}

////////////////////////////////////////////////////////////////////////////////
// Section 11. Type-Safe Messages using Generics
////////////////////////////////////////////////////////////////////////////////

type TypedMessage[T any] struct {
	Payload T
}

func NewTypedMessage[T any](payload T) TypedMessage[T] {
	return TypedMessage[T]{Payload: payload}
}

////////////////////////////////////////////////////////////////////////////////
// Section 12. ActorContext Definition
////////////////////////////////////////////////////////////////////////////////

type ActorContext struct {
	Self       ActorRef
	Parent     *ActorRef
	Context    context.Context
	CancelFunc context.CancelFunc
}

////////////////////////////////////////////////////////////////////////////////
// Section 13. Health Check Endpoint for Operational Monitoring
////////////////////////////////////////////////////////////////////////////////

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
			structuredLog("health", "Health endpoint error", map[string]any{"error": err.Error()})
		}
	}()
}

////////////////////////////////////////////////////////////////////////////////
// Section 14. Example Actor Implementations
////////////////////////////////////////////////////////////////////////////////

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

////////////////////////////////////////////////////////////////////////////////
// Section X. HTTP Actor for Fiber Integration
////////////////////////////////////////////////////////////////////////////////

type HttpRequestMessage struct {
	HTTPMethod  string
	Path        string
	QueryParams map[string]string
	Body        []byte
	ReplyChan   chan string
}

type HttpActor struct {
	BaseActor
}

func (a *HttpActor) PreStart(ctx ActorContext) {
	structuredLog("actor", "HttpActor starting", map[string]any{"actor": ctx.Self.pid})
	a.Become(a.handleRequest)
}

func (a *HttpActor) handleRequest(ctx ActorContext, msg Message) {
	// Unwrap the message if it is an Envelope.
	var reqMsg HttpRequestMessage
	switch m := msg.(type) {
	case Envelope:
		var ok bool
		reqMsg, ok = m.Message.(HttpRequestMessage)
		if !ok {
			structuredLog("actor", "HttpActor received invalid envelope payload", map[string]any{"actor": ctx.Self.pid})
			return
		}
	case HttpRequestMessage:
		reqMsg = m
	default:
		structuredLog("actor", "HttpActor received unknown message type", map[string]any{"actor": ctx.Self.pid})
		return
	}
	// Generate a response with current timestamp.
	response := fmt.Sprintf("Handled %s request for %s at %s", reqMsg.HTTPMethod, reqMsg.Path, time.Now().Format(time.RFC3339))
	reqMsg.ReplyChan <- response
}

func (a *HttpActor) Receive(ctx ActorContext, msg Message) {
	a.DefaultReceive(ctx, msg)
}

func (a *HttpActor) PostStop(ctx ActorContext) {
	structuredLog("actor", "HttpActor stopping", map[string]any{"actor": ctx.Self.pid})
}

func (a *HttpActor) PreRestart(ctx ActorContext, reason error) {
	structuredLog("actor", "HttpActor restarting", map[string]any{"actor": ctx.Self.pid, "reason": reason.Error()})
}

////////////////////////////////////////////////////////////////////////////////
// Section 15. Main Function (Updated for Fiber HTTP Server)
////////////////////////////////////////////////////////////////////////////////

func main() {
	// Load configuration (assumes config.yaml in working directory).
	cfg, err := LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	// Save start time in context for health checks.
	ctx := context.WithValue(context.Background(), "startTime", time.Now())

	// Create actor system.
	system := NewActorSystem(cfg)
	system.ctx = ctx

	// Start health check server.
	system.ServeHealthCheck(cfg.HealthCheckEndpoint)

	// Compose interceptors.
	interceptors := []MessageInterceptor{
		LoggingInterceptor,
		RateLimitingInterceptor(cfg.RateLimitPerSecond),
	}

	// Spawn an EchoActor.
	echoProps := ActorProps{
		MailboxSize:  cfg.MailboxSize,
		MailboxType:  MailboxStandard,
		Interceptors: interceptors,
	}
	echo := &EchoActor{name: "Echo1"}
	echoRef := system.Spawn("echo1", echo, echoProps)

	// Spawn additional EchoActors for routing.
	var poolRefs []*ActorRef
	for i := 2; i <= 4; i++ {
		a := &EchoActor{name: fmt.Sprintf("Echo%d", i)}
		ref := system.Spawn(fmt.Sprintf("echo%d", i), a, echoProps)
		poolRefs = append(poolRefs, ref)
	}

	// Create a round-robin router for EchoActors.
	router := NewRoundRobinRouter(poolRefs)

	// Demonstrate Tell and Ask.
	if err := echoRef.Tell("Hello, Actor!"); err != nil {
		structuredLog("main", "Failed to send message", map[string]any{"error": err.Error()})
	}
	resp, err := echoRef.Ask("Ping", 3*time.Second)
	if err != nil {
		structuredLog("main", "Ask error", map[string]any{"error": err.Error()})
	} else {
		structuredLog("main", "Received response", map[string]any{"response": resp})
	}

	// Route some messages via router.
	for i := 0; i < 5; i++ {
		router.Route(fmt.Sprintf("Message #%d", i+1))
	}

	// Spawn a TimerActor.
	timerActor := &TimerActor{name: "Timer1"}
	system.Spawn("timer1", timerActor, ActorProps{
		MailboxSize:  cfg.MailboxSize,
		MailboxType:  MailboxStandard,
		Interceptors: interceptors,
	})

	// Spawn a PersistentActor.
	persistentActor := &PersistentActor{name: "Persist1"}
	system.Spawn("persist1", persistentActor, ActorProps{
		MailboxSize:  cfg.MailboxSize + 5,
		MailboxType:  MailboxStandard,
		Interceptors: interceptors,
	})

	// Pub/Sub demonstration: subscribe EchoActor to topic "news".
	globalPubSub.Subscribe("news", echoRef)
	globalPubSub.Publish("news", "Breaking: New Actor Framework Released!")

	////////////////////////////////////////////////////////////////////////////////
	// Fiber HTTP Server Integration
	////////////////////////////////////////////////////////////////////////////////

	app := fiber.New()

	// Create an HTTP actor pool.
	var httpActors []*ActorRef
	for i := 1; i <= 3; i++ {
		httpActor := &HttpActor{}
		ref := system.Spawn(fmt.Sprintf("http%d", i), httpActor, ActorProps{
			MailboxSize:  cfg.MailboxSize,
			MailboxType:  MailboxStandard,
			Interceptors: interceptors,
		})
		httpActors = append(httpActors, ref)
	}
	// Create a round-robin router for HTTP actors.
	httpRouter := NewRoundRobinRouter(httpActors)

	// Define a catch-all handler.
	app.All("/*", func(c *fiber.Ctx) error {
		replyChan := make(chan string, 1)
		reqMsg := HttpRequestMessage{
			HTTPMethod:  c.Method(),
			Path:        c.Path(),
			QueryParams: map[string]string{}, // For brevity; you could extract query params.
			Body:        c.Body(),
			ReplyChan:   replyChan,
		}
		// Manually select an actor using round-robin.
		actorIdx := int(atomic.AddUint64(&httpRouter.counter, 1)) % len(httpActors)
		resp, err := httpActors[actorIdx].Ask(reqMsg, 3*time.Second)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("Error processing request")
		}
		responseStr, ok := resp.Data.(string)
		if !ok {
			responseStr = "Invalid response"
		}
		return c.SendString(responseStr)
	})

	// Start the Fiber server.
	go func() {
		if err := app.Listen(":3000"); err != nil {
			structuredLog("fiber", "Fiber server error", map[string]any{"error": err.Error()})
		}
	}()
	structuredLog("main", "Fiber HTTP server started on :3000", nil)

	// Let the whole system run.
	time.Sleep(30 * time.Second)

	// Gracefully shutdown the Fiber server and the actor system.
	_ = app.Shutdown()
	structuredLog("main", "Shutting down actor system...", nil)
	system.Shutdown()
	structuredLog("main", "Actor system shutdown complete.", nil)
}
