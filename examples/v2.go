// actor_framework.go
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
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================================
// Section 1: Actor Interface, Dynamic Behavior & BaseActor
// ============================================================================

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

// Behavior defines a function for dynamic behavior.
type Behavior func(ctx ActorContext, msg Message)

// BaseActor provides default behavior switching support.
type BaseActor struct {
	mu            sync.Mutex
	behavior      Behavior
	behaviorStack []Behavior
}

func (b *BaseActor) Become(newBehavior Behavior) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Push the current behavior onto the stack if it exists.
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

// ============================================================================
// Section 2: Persistence & Event Sourcing
// ============================================================================

type Persistence interface {
	SaveSnapshot(actorID string, state any) error
	RestoreSnapshot(actorID string) (any, error)
}

type DummyPersistence struct {
	snapshots sync.Map
}

func (p *DummyPersistence) SaveSnapshot(actorID string, state any) error {
	p.snapshots.Store(actorID, state)
	log.Printf("[Persistence] Snapshot saved for %s", actorID)
	return nil
}

func (p *DummyPersistence) RestoreSnapshot(actorID string) (any, error) {
	if state, ok := p.snapshots.Load(actorID); ok {
		log.Printf("[Persistence] Snapshot restored for %s", actorID)
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
	log.Printf("[EventLogger] Logged event for %s", actorID)
	return nil
}

func (l *DummyEventLogger) GetEvents(actorID string) ([]any, error) {
	if events, ok := l.events.Load(actorID); ok {
		return events.([]any), nil
	}
	return nil, errors.New("no events found")
}

var eventLogger EventLogger = &DummyEventLogger{}

// ============================================================================
// Section 3: Pub/Sub Mechanism
// ============================================================================

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
			log.Printf("[PubSub] Error delivering to %s: %v", ref.pid, err)
		}
	}
}

var globalPubSub = NewPubSub()

// ============================================================================
// Section 4: Middleware / Interceptors & Metrics
// ============================================================================

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
	log.Printf("[Metrics] Incremented %s", metric)
}

func (m *DummyMetrics) Observe(metric string, value float64) {
	log.Printf("[Metrics] Observed %s: %f", metric, value)
}

var metrics Metrics = &DummyMetrics{}

// LoggingInterceptor logs messages before processing.
func LoggingInterceptor(next func(ctx ActorContext, msg Message)) func(ctx ActorContext, msg Message) {
	return func(ctx ActorContext, msg Message) {
		log.Printf("[Interceptor] Actor %s processing message: %+v", ctx.Self.pid, msg)
		metrics.Increment("messages.processed")
		next(ctx, msg)
	}
}

// RateLimitingInterceptor limits processing to a maximum number per window.
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
			defer mu.Unlock()
			if count >= maxPerSec {
				log.Printf("[Interceptor] Actor %s rate limit exceeded. Dropping message: %+v", ctx.Self.pid, msg)
				metrics.Increment("messages.dropped")
				return
			}
			count++
			next(ctx, msg)
		}
	}
}

// ============================================================================
// Section 5: Actor Addressing and Mailbox Implementations
// ============================================================================

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
	return r.mailbox.Enqueue(message)
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
	// Try multiple times before giving up
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

// PriorityMailbox using heap for prioritized message handling.
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
		p = 1000 // default lowest priority
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

// ============================================================================
// Section 6: Actor Cell & Execution with Middleware
// ============================================================================

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
		// Recover from panics for fault tolerance.
		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("actor %s panicked: %v", cell.ref.pid, r)
				log.Printf("[ActorSystem] %s", err)
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

// ============================================================================
// Section 7: Supervision & Advanced Restart Strategy
// ============================================================================

type SupervisionStrategy interface {
	HandleFailure(child *actorCell, reason error)
}

type OneForOneStrategy struct {
	maxRestarts int
	backoff     time.Duration
	restarts    sync.Map // map of actorID to restart count
}

func (s *OneForOneStrategy) HandleFailure(child *actorCell, reason error) {
	actorID := child.ref.pid
	countAny, _ := s.restarts.LoadOrStore(actorID, 0)
	count := countAny.(int)
	if count >= s.maxRestarts {
		log.Printf("[Supervisor] Actor %s exceeded maximum restarts. Not restarting.", actorID)
		// Optionally, perform cleanup or escalate.
		return
	}
	log.Printf("[Supervisor] Actor %s failed: %v. Restarting with backoff...", actorID, reason)
	// Exponential backoff.
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

// ============================================================================
// Section 8: Actor System, Registry & Remote Transport Stub
// ============================================================================

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

// JSONRemoteTransport is a simple remote transport using JSON over HTTP.
type JSONRemoteTransport struct {
	endpoint string
	client   *http.Client
}

func (t *JSONRemoteTransport) SendRemote(actorID string, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("serialization error: %w", err)
	}
	// In a production system, you would send the message to the remote endpoint.
	log.Printf("[RemoteTransport] Sending to %s: %s", actorID, string(data))
	// Stubbed HTTP POST (error handling omitted for brevity)
	http.Post(t.endpoint, "application/json", nil)
	return nil
}

type ActorSystem struct {
	registry        sync.Map // map[string]*actorCell
	ctx             context.Context
	cancel          context.CancelFunc
	supervisor      SupervisionStrategy
	remoteTransport RemoteTransport
}

func NewActorSystem() *ActorSystem {
	ctx, cancel := context.WithCancel(context.Background())
	// For demonstration, maximum 5 restarts and a base backoff of 1 sec.
	supervisor := &OneForOneStrategy{maxRestarts: 5, backoff: 1 * time.Second}
	return &ActorSystem{
		ctx:             ctx,
		cancel:          cancel,
		supervisor:      supervisor,
		remoteTransport: &JSONRemoteTransport{endpoint: "http://localhost:8080/remote", client: &http.Client{}},
	}
}

func NewTestActorSystem() *ActorSystem {
	log.Println("[Config] Loading test configuration...")
	return NewActorSystem()
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
			capacity = 10
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
	// Compose interceptors if provided.
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
	return nil
}

func (sys *ActorSystem) Shutdown() {
	sys.registry.Range(func(key, value any) bool {
		cell := value.(*actorCell)
		cell.stop()
		sys.registry.Delete(key)
		log.Printf("[Shutdown] Actor %s stopped", cell.ref.pid)
		return true
	})
	sys.cancel()
	time.Sleep(500 * time.Millisecond)
	log.Println("[Shutdown] Actor system shutdown complete with graceful cleanup.")
}

// ============================================================================
// Section 9: Router Implementations (Round-Robin & Consistent Hashing Stub)
// ============================================================================

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
		log.Printf("[Router] Error sending message: %v", err)
	}
}

// ConsistentHashRouter stub: can be extended to support routing based on key hash.
type ConsistentHashRouter struct {
	actors []*ActorRef
}

func NewConsistentHashRouter(actors []*ActorRef) *ConsistentHashRouter {
	return &ConsistentHashRouter{actors: actors}
}

func (r *ConsistentHashRouter) RouteByKey(message Message, key string) {
	// Compute hash from key and route accordingly.
	hash := int(0)
	for _, b := range key {
		hash += int(b)
	}
	index := hash % len(r.actors)
	if err := r.actors[index].Tell(message); err != nil {
		log.Printf("[ConsistentHashRouter] Error sending message: %v", err)
	}
}

// ============================================================================
// Section 10: Type-Safe Messages using Generics
// ============================================================================

type TypedMessage[T any] struct {
	Payload T
}

func NewTypedMessage[T any](payload T) TypedMessage[T] {
	return TypedMessage[T]{Payload: payload}
}

// ============================================================================
// Section 11: ActorContext Definition
// ============================================================================

type ActorContext struct {
	Self       ActorRef
	Parent     *ActorRef
	Context    context.Context
	CancelFunc context.CancelFunc
}

// ============================================================================
// Section 12: Example Actor Implementations
// ============================================================================

type EchoActor struct {
	name string
	BaseActor
}

func (a *EchoActor) PreStart(ctx ActorContext) {
	log.Printf("[EchoActor:%s] Starting up", a.name)
	a.Become(func(ctx ActorContext, msg Message) {
		switch env := msg.(type) {
		case Envelope:
			log.Printf("[EchoActor:%s] Received Envelope message: %+v", a.name, env.Message)
			if env.ReplyChan != nil {
				respData, _ := json.Marshal(fmt.Sprintf("Echo: %v", env.Message))
				env.ReplyChan <- ResponseMessage{Data: string(respData)}
			}
		default:
			log.Printf("[EchoActor:%s] Received message: %+v", a.name, msg)
		}
	})
}

func (a *EchoActor) Receive(ctx ActorContext, msg Message) {
	a.DefaultReceive(ctx, msg)
}

func (a *EchoActor) PostStop(ctx ActorContext) {
	log.Printf("[EchoActor:%s] Stopping", a.name)
}

func (a *EchoActor) PreRestart(ctx ActorContext, reason error) {
	log.Printf("[EchoActor:%s] Restarting due to: %v", a.name, reason)
}

type TimerActor struct {
	name string
}

func (a *TimerActor) PreStart(ctx ActorContext) {
	log.Printf("[TimerActor:%s] Started", a.name)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Context.Done():
				return
			case <-ticker.C:
				err := ctx.Self.Tell("tick")
				if err != nil {
					log.Printf("[TimerActor:%s] Error scheduling tick: %v", a.name, err)
				}
			}
		}
	}()
}

func (a *TimerActor) Receive(ctx ActorContext, msg Message) {
	log.Printf("[TimerActor:%s] Received message: %v", a.name, msg)
}

func (a *TimerActor) PostStop(ctx ActorContext) {
	log.Printf("[TimerActor:%s] Stopped", a.name)
}

func (a *TimerActor) PreRestart(ctx ActorContext, reason error) {
	log.Printf("[TimerActor:%s] Restarting due to: %v", a.name, reason)
}

type PersistentActor struct {
	name  string
	state map[string]any
	BaseActor
}

func (a *PersistentActor) PreStart(ctx ActorContext) {
	log.Printf("[PersistentActor:%s] Starting up", a.name)
	if restored, err := persistenceManager.RestoreSnapshot(a.name); err == nil {
		a.state = restored.(map[string]any)
	} else {
		a.state = make(map[string]any)
	}
	a.Become(func(ctx ActorContext, msg Message) {
		log.Printf("[PersistentActor:%s] Processing message: %v", a.name, msg)
		a.state["lastMessage"] = msg
		_ = persistenceManager.SaveSnapshot(a.name, a.state)
		_ = eventLogger.LogEvent(a.name, msg)
	})
}

func (a *PersistentActor) Receive(ctx ActorContext, msg Message) {
	a.DefaultReceive(ctx, msg)
}

func (a *PersistentActor) PostStop(ctx ActorContext) {
	log.Printf("[PersistentActor:%s] Stopping", a.name)
}

func (a *PersistentActor) PreRestart(ctx ActorContext, reason error) {
	log.Printf("[PersistentActor:%s] Restarting due to: %v", a.name, reason)
}

// ============================================================================
// Section 13: Main Function (Demonstration & Testing)
// ============================================================================

func main() {
	system := NewActorSystem()

	// Combine logging and rate-limiting interceptors.
	interceptors := []MessageInterceptor{
		LoggingInterceptor,
		RateLimitingInterceptor(10), // Maximum 10 messages per second per actor.
	}

	// Spawn an EchoActor.
	echoProps := ActorProps{
		MailboxSize:  20,
		MailboxType:  MailboxStandard,
		Interceptors: interceptors,
	}
	echo := &EchoActor{name: "Echo1"}
	echoRef := system.Spawn("echo1", echo, echoProps)

	// Spawn additional EchoActors for round-robin routing.
	var poolRefs []*ActorRef
	for i := 2; i <= 4; i++ {
		a := &EchoActor{name: fmt.Sprintf("Echo%d", i)}
		ref := system.Spawn(fmt.Sprintf("echo%d", i), a, echoProps)
		poolRefs = append(poolRefs, ref)
	}

	// Create a round-robin router.
	router := NewRoundRobinRouter(poolRefs)

	// Use Tell and Ask patterns.
	if err := echoRef.Tell("Hello, Actor!"); err != nil {
		log.Printf("Failed to send message: %v", err)
	}
	resp, err := echoRef.Ask("Ping", 3*time.Second)
	if err != nil {
		log.Printf("Ask error: %v", err)
	} else {
		log.Printf("Received response: %+v", resp)
	}

	// Send messages via router.
	for i := 0; i < 5; i++ {
		router.Route(fmt.Sprintf("Message #%d", i+1))
	}

	// Spawn a TimerActor.
	timerActor := &TimerActor{name: "Timer1"}
	system.Spawn("timer1", timerActor, ActorProps{
		MailboxSize:  10,
		MailboxType:  MailboxStandard,
		Interceptors: interceptors,
	})

	// Spawn a PersistentActor.
	persistentActor := &PersistentActor{name: "Persist1"}
	system.Spawn("persist1", persistentActor, ActorProps{
		MailboxSize:  15,
		MailboxType:  MailboxStandard,
		Interceptors: interceptors,
	})

	// Pub/Sub: subscribe echoRef to topic "news".
	globalPubSub.Subscribe("news", echoRef)
	// Publish a message on topic "news".
	globalPubSub.Publish("news", "Breaking: New Actor Framework Released!")

	// Let the system run for some time.
	time.Sleep(10 * time.Second)

	// Initiate graceful shutdown.
	log.Println("Shutting down actor system...")
	system.Shutdown()
	log.Println("Actor system shutdown complete.")
}
