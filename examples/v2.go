package main

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	recoverMw "github.com/gofiber/fiber/v2/middleware/recover"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v2"
)

type contextKey string

const startTimeKey contextKey = "startTime"

var logger *zap.Logger
var actorRestartCount atomic.Int64

func initLogger() {
	var err error

	logger, err = zap.NewProduction(zap.AddCaller())
	if err != nil {
		logger, err = zap.NewDevelopment(zap.AddCaller())
		if err != nil {
			log.Fatalf("Failed to initialize any logger: %v", err)
		}
	}
	rand.Seed(time.Now().UnixNano())
}

type Config struct {
	MailboxSize         int           `yaml:"mailbox_size"`
	MaxRestarts         int           `yaml:"max_restarts"`
	BackoffBase         time.Duration `yaml:"backoff_base"`
	RateLimitPerSecond  int           `yaml:"rate_limit_per_second"`
	HealthCheckEndpoint string        `yaml:"health_check_endpoint"`
	RemoteEndpoint      string        `yaml:"remote_endpoint"`
	HTTPPort            string        `yaml:"http_port"`
}

func (cfg *Config) Validate() error {
	if cfg.HTTPPort == "" {
		return errors.New("HTTPPort must not be empty")
	}
	// ...additional validations as needed...
	return nil
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
	if err := cfg.Validate(); err != nil {
		return nil, err
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
		corrID := ""
		if id := ctx.Context.Value("correlation_id"); id != nil {
			corrID = fmt.Sprintf("%v", id)
		}
		structuredLog("interceptor", "Processing message", map[string]any{
			"actor": ctx.Self.pid, "message": msg, "correlation_id": corrID,
		})
		metrics.Increment("messages.processed")
		next(ctx, msg)
	}
}

func RateLimitingInterceptor(maxPerSec int) MessageInterceptor {
	limiter := rate.NewLimiter(rate.Limit(maxPerSec), maxPerSec)
	return func(next func(ctx ActorContext, msg Message)) func(ctx ActorContext, msg Message) {
		return func(ctx ActorContext, msg Message) {
			if !limiter.Allow() {
				structuredLog("interceptor", "Rate limit exceeded", map[string]any{"actor": ctx.Self.pid, "message": msg})
				metrics.Increment("messages.dropped")
				return
			}
			next(ctx, msg)
		}
	}
}

func MetricsInterceptor(next func(ctx ActorContext, msg Message)) func(ctx ActorContext, msg Message) {
	return func(ctx ActorContext, msg Message) {
		startTime := time.Now()
		next(ctx, msg)
		duration := time.Since(startTime)
		metrics.Observe("actor.message.processing.duration_ms", float64(duration.Milliseconds()))
	}
}

var (
	deadLetterQueue = make(chan Message, 10000)
	deadLetterWg    sync.WaitGroup
)

func startDeadLetterProcessor() {
	deadLetterWg.Add(1)
	go func() {
		defer deadLetterWg.Done()
		for msg := range deadLetterQueue {
			structuredLog("deadletter", "Processing dead-letter message", map[string]any{"message": msg})
		}
	}()
}

func stopDeadLetterProcessor() {
	close(deadLetterQueue)
	deadLetterWg.Wait()
}

func enqueueDeadLetter(msg Message) {
	select {
	case deadLetterQueue <- msg:
	default:
	}
}

func structuredLog(component, msg string, fields map[string]any) {
	if logger != nil {
		logger.Info(msg, zap.String("component", component), zap.Any("fields", fields))
		return
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["timestamp"] = time.Now().Format(time.RFC3339)
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
	maxRetries := 3
	delay := 50 * time.Millisecond
	for i := 0; i < maxRetries; i++ {
		select {
		case m.ch <- message:
			return nil
		default:
			time.Sleep(delay)
			delay *= 2
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

// Add a pool for PriorityMessage to reduce allocations
var priorityMessagePool = sync.Pool{
	New: func() any {
		return new(PriorityMessage)
	},
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

// Modify PriorityMailbox.Enqueue to obtain a PriorityMessage from the pool.
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
	// Use pooled PriorityMessage
	pmsg := priorityMessagePool.Get().(*PriorityMessage)
	pmsg.priority = p
	pmsg.message = message
	heap.Push(&pm.messages, pmsg)
	pm.cond.Signal()
	return nil
}

// Modify PriorityMailbox.Dequeue to return the message and recycle the PriorityMessage.
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
	msg := pmsg.message
	// Reset and return the PriorityMessage to the pool
	pmsg.priority, pmsg.message, pmsg.index = 0, nil, 0
	priorityMessagePool.Put(pmsg)
	return msg, true
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

				structuredLog("actor_system", "Panic recovered", map[string]any{
					"actor": cell.ref.pid, "error": err.Error(), "stack": string(debug.Stack()),
				})
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
	structuredLog("supervision", "Scheduling actor restart", map[string]any{"actor": actorID, "attempt": count + 1})

	actorRestartCount.Add(1)

	jitter := time.Duration(rand.Int63n(int64(s.backoff)))
	delay := time.Duration(math.Pow(2, float64(count)))*s.backoff + jitter

	go func() {
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
	}()
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

// Enhance JSONRemoteTransport with a configurable failure threshold for its circuit breaker
type JSONRemoteTransport struct {
	endpoint         string
	client           *http.Client
	failureCount     int
	circuitOpen      bool
	lastFailure      time.Time
	breakerTimeout   time.Duration
	FailureThreshold int // new field: number of failures before opening the circuit
}

// In SendRemote, use the new FailureThreshold field.
func (t *JSONRemoteTransport) SendRemote(actorID string, msg Message) error {
	if t.circuitOpen {
		if time.Since(t.lastFailure) < t.breakerTimeout {
			structuredLog("remote", "Circuit breaker open, skipping remote call", map[string]any{"actor": actorID})
			return fmt.Errorf("circuit breaker open: skipping remote call")
		} else {
			t.circuitOpen = false
			t.failureCount = 0
			structuredLog("remote", "Circuit breaker reset", map[string]any{"actor": actorID})
		}
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("serialization error: %w", err)
	}
	maxRetries := 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		structuredLog("remote", "Attempting remote call", map[string]any{"actor": actorID, "attempt": attempt + 1})
		resp, err := t.client.Post(t.endpoint, "application/json", bytes.NewReader(data))
		if err != nil {
			lastErr = err
		} else {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				t.failureCount = 0
				return nil
			} else {
				lastErr = fmt.Errorf("remote endpoint returned status: %s, response: %s", resp.Status, string(bodyBytes))
			}
		}
		baseDelay := 100 * time.Millisecond
		backoff := time.Duration(math.Pow(2, float64(attempt))) * baseDelay
		jitter := time.Duration(rand.Int63n(50 * int64(time.Millisecond)))
		time.Sleep(backoff + jitter)
	}
	t.failureCount++
	t.lastFailure = time.Now()
	if t.failureCount >= t.FailureThreshold {
		t.circuitOpen = true
		structuredLog("remote", "Circuit breaker opened", map[string]any{"actor": actorID, "failureCount": t.failureCount})
	}
	structuredLog("remote", "Failed remote message after retries", map[string]any{"actor": actorID, "maxRetries": maxRetries, "lastError": lastErr.Error()})
	return fmt.Errorf("failed to send remote message after %d attempts: %w", maxRetries, lastErr)
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

// In NewActorSystem, initialize JSONRemoteTransport with the desired failure threshold.
func NewActorSystem(cfg *Config) *ActorSystem {
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := &OneForOneStrategy{maxRestarts: cfg.MaxRestarts, backoff: cfg.BackoffBase}
	startTimeCtx := context.WithValue(ctx, startTimeKey, time.Now())
	return &ActorSystem{
		ctx:        startTimeCtx,
		cancel:     cancel,
		supervisor: supervisor,
		remoteTransport: &JSONRemoteTransport{
			endpoint:         cfg.RemoteEndpoint,
			client:           &http.Client{Timeout: 10 * time.Second},
			breakerTimeout:   5 * time.Second,
			FailureThreshold: 3, // set failure threshold
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
	stopDeadLetterProcessor()
	sys.cancel()
	time.Sleep(100 * time.Millisecond)
	structuredLog("shutdown", "Actor system shutdown complete", nil)
}

func (sys *ActorSystem) ServeHealthCheck(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		startTime, _ := sys.ctx.Value(startTimeKey).(time.Time)
		status := map[string]any{
			"active_actors": sys.activeActors.Load(),
			"uptime":        time.Since(startTime).String(),
		}
		data, _ := json.Marshal(status)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metricsData := map[string]any{
			"active_actors": sys.activeActors.Load(),
			"start_time":    sys.ctx.Value(startTimeKey),
		}
		data, _ := json.Marshal(metricsData)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})
	go func() {
		structuredLog("health", "Starting health endpoint", map[string]any{"address": addr})
		if err := http.ListenAndServe(addr, mux); err != nil {
			structuredLog("health", "Endpoint error", map[string]any{"error": err.Error()})
		}
	}()
}

// Add new method to list actors in the actor system.
func (sys *ActorSystem) ListActors() []map[string]any {
	var actors []map[string]any
	sys.registry.Range(func(key, value any) bool {
		cell := value.(*actorCell)
		actors = append(actors, map[string]any{
			"pid":         cell.ref.pid,
			"mailboxType": cell.props.MailboxType,
			// Additional metrics can be added here.
		})
		return true
	})
	return actors
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
	ID            string
	Payload       string
	Reply         chan ResponseMessage
	CorrelationID string
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

	configPath := flag.String("config", "config.yaml", "Path to the configuration file")
	flag.Parse()

	initLogger()

	defer logger.Sync()
	startedAt := time.Now()
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Fatal("Failed to load config", zap.Error(err))
	}
	ctx := context.WithValue(context.Background(), startTimeKey, startedAt)
	startDeadLetterProcessor()
	system := NewActorSystem(cfg)
	system.ctx = ctx
	system.ServeHealthCheck(cfg.HealthCheckEndpoint)
	interceptors := []MessageInterceptor{
		LoggingInterceptor,
		RateLimitingInterceptor(cfg.RateLimitPerSecond),
		MetricsInterceptor,
	}

	app := fiber.New(fiber.Config{
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  10 * time.Second,
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			structuredLog("fiber", "Unhandled error", map[string]any{"error": err.Error()})
			return c.Status(fiber.StatusInternalServerError).SendString("Internal Server Error")
		},
	})

	// Add middleware to extract correlation id from the header.
	app.Use(func(c *fiber.Ctx) error {
		if corrID := c.Get("X-Correlation-ID"); corrID != "" {
			c.Locals("correlation_id", corrID)
		}
		return c.Next()
	})
	app.Use(recoverMw.New())
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
		data, ok := payload["data"].(string)
		if !ok || data == "" {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid or missing 'data' field")
		}
		reqID := fmt.Sprintf("%d", time.Now().UnixNano())

		corrID := c.Get("X-Correlation-ID", "")
		replyChan := make(chan ResponseMessage, 1)
		reqMsg := RequestMessage{
			ID:            reqID,
			Payload:       data,
			Reply:         replyChan,
			CorrelationID: corrID,
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
		return c.JSON(map[string]any{
			"active_actors": system.activeActors.Load(),
			"start_time":    system.ctx.Value(startTimeKey),
		})
	})

	// New routes for admin UI and API for actor monitoring and management.
	app.Get("/admin/ui", func(c *fiber.Ctx) error {
		// Serve the static HTML file for the admin UI.
		return c.Type("html").SendFile("/Users/sujit/Sites/go-app/admin.html")
	})

	// API: List actors
	app.Get("/admin/api/actors", func(c *fiber.Ctx) error {
		actors := system.ListActors()
		return c.JSON(actors)
	})
	// API: Stop an actor by its id
	app.Post("/admin/api/actors/:id/stop", func(c *fiber.Ctx) error {
		actorID := c.Params("id")
		if err := system.Stop(actorID); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(map[string]any{"error": err.Error()})
		}
		return c.JSON(map[string]any{"message": "Actor " + actorID + " stopped"})
	})
	// Optionally more actor operations (restart, etc.) can be added here.

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := app.Listen(cfg.HTTPPort); err != nil {
			logger.Fatal("Fiber server error", zap.Error(err))
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

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	structuredLog("shutdown", "Memory stats", map[string]any{
		"Alloc":      m.Alloc,
		"TotalAlloc": m.TotalAlloc,
		"Sys":        m.Sys,
		"NumGC":      m.NumGC,
	})
	<-shutdownCtx.Done()
	structuredLog("shutdown", "Shutdown signal received", map[string]any{"signal": "context cancellation"})
	// Trigger a graceful shutdown for Fiber and the actor system.
	shutdownCtxTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(shutdownCtxTimeout); err != nil {
		structuredLog("shutdown", "Fiber shutdown error", map[string]any{"error": err.Error()})
	}
	system.Shutdown()
	os.Exit(0)
}
