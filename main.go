package events

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
	
	"github.com/oarkflow/maps"
)

type subId uint64

const (
	defaultBroadcastTimeout = time.Minute
)

type Storage[T any] interface {
	Store(event T) error
	Load(subId) ([]T, error)
}

type RetryPolicy interface {
	ShouldRetry(err error) bool
	RetryDelay() time.Duration
}

type DeliveryGuarantee interface {
	Deliver(ctx context.Context, event interface{}, sendFunc func() error) error
}

// Producer manages event subscriptions and broadcasts events to them.
type Producer[T any] struct {
	subs              maps.IMap[subId, *Subscription[T]]
	nextID            subId
	doneListener      chan subId    // channel to listen for IDs of subscriptions to be removed.
	broadcastTimeout  time.Duration // maximum duration to wait for an event to be sent.
	storage           Storage[T]
	retryPolicy       RetryPolicy
	deliveryGuarantee DeliveryGuarantee
}

type ProducerOpt[T any] func(*Producer[T])

// WithBroadcastTimeout sets the amount of time the broadcaster will wait to send
// to each subscriber before dropping the send.
func WithBroadcastTimeout[T any](timeout time.Duration) ProducerOpt[T] {
	return func(ep *Producer[T]) {
		ep.broadcastTimeout = timeout
	}
}

// WithStorage sets the storage mechanism for events.
func WithStorage[T any](storage Storage[T]) ProducerOpt[T] {
	return func(ep *Producer[T]) {
		ep.storage = storage
	}
}

// WithRetryPolicy sets the retry policy for event delivery.
func WithRetryPolicy[T any](retryPolicy RetryPolicy) ProducerOpt[T] {
	return func(ep *Producer[T]) {
		ep.retryPolicy = retryPolicy
	}
}

// WithDeliveryGuarantee sets the delivery guarantee strategy for event delivery.
func WithDeliveryGuarantee[T any](guarantee DeliveryGuarantee) ProducerOpt[T] {
	return func(ep *Producer[T]) {
		ep.deliveryGuarantee = guarantee
	}
}

func NewProducer[T any](opts ...ProducerOpt[T]) *Producer[T] {
	producer := &Producer[T]{
		subs:             maps.New[subId, *Subscription[T]](),
		doneListener:     make(chan subId, 100),
		broadcastTimeout: defaultBroadcastTimeout,
	}
	for _, opt := range opts {
		opt(producer)
	}
	return producer
}

// Start begins listening for subscription cancelation requests or context cancelation.
func (ep *Producer[T]) Start(ctx context.Context) {
	for {
		select {
		case id := <-ep.doneListener:
			if sub, exists := ep.subs.Get(id); exists {
				close(sub.events)
				ep.subs.Del(id)
			}
		case <-ctx.Done():
			close(ep.doneListener)
			return
		}
	}
}

func (ep *Producer[T]) Subscribe(bufferSize int) *Subscription[T] {
	id := ep.nextID
	ep.nextID++
	sub := &Subscription[T]{
		id:     id,
		events: make(chan T, bufferSize),
		done:   ep.doneListener,
	}
	ep.subs.Set(id, sub)
	
	// Load past events from storage if any
	if ep.storage != nil {
		events, err := ep.storage.Load(id)
		if err != nil {
			log.Printf("Failed to load events for subscriber %d: %v", id, err)
		} else {
			go func() {
				for _, event := range events {
					sub.events <- event
				}
			}()
		}
	}
	
	return sub
}

func (ep *Producer[T]) Broadcast(ctx context.Context, event T) {
	var wg sync.WaitGroup
	ep.subs.ForEach(func(_ subId, sub *Subscription[T]) bool {
		wg.Add(1)
		go func(listener *Subscription[T], w *sync.WaitGroup) {
			defer w.Done()
			ep.sendEvent(ctx, listener, event)
		}(sub, &wg)
		return true
	})
	wg.Wait()
}

func (ep *Producer[T]) sendEvent(ctx context.Context, listener *Subscription[T], event T) {
	sendFunc := func() error {
		select {
		case listener.events <- event:
			return nil
		case <-time.After(ep.broadcastTimeout):
			return fmt.Errorf("broadcast to subscriber %d timed out", listener.id)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	
	if ep.deliveryGuarantee != nil {
		if err := ep.deliveryGuarantee.Deliver(ctx, event, sendFunc); err != nil {
			log.Printf("Failed to deliver event to subscriber %d: %v", listener.id, err)
		}
	} else {
		if err := sendFunc(); err != nil {
			log.Printf("Failed to deliver event to subscriber %d: %v", listener.id, err)
		}
	}
	
	if ep.storage != nil {
		if err := ep.storage.Store(event); err != nil {
			log.Printf("Failed to store event: %v", err)
		}
	}
}

type Subscription[T any] struct {
	id     subId
	events chan T
	done   chan subId
}

func (es *Subscription[T]) Next(ctx context.Context) (T, error) {
	var zeroVal T
	select {
	case ev := <-es.events:
		return ev, nil
	case <-ctx.Done():
		es.done <- es.id
		return zeroVal, ctx.Err()
	}
}

// Example implementations of Storage, RetryPolicy, and DeliveryGuarantee

// MemoryStorage is a simple in-memory storage.
type MemoryStorage[T any] struct {
	store map[subId][]T
	mu    sync.Mutex
}

func NewMemoryStorage[T any]() *MemoryStorage[T] {
	return &MemoryStorage[T]{store: make(map[subId][]T)}
}

func (ms *MemoryStorage[T]) Store(event T) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	for subID := range ms.store {
		ms.store[subID] = append(ms.store[subID], event)
	}
	return nil
}

func (ms *MemoryStorage[T]) Load(id subId) ([]T, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.store[id], nil
}

// SimpleRetryPolicy retries on all errors with a fixed delay.
type SimpleRetryPolicy struct {
	delay time.Duration
}

func NewSimpleRetryPolicy(delay time.Duration) *SimpleRetryPolicy {
	return &SimpleRetryPolicy{delay: delay}
}

func (srp *SimpleRetryPolicy) ShouldRetry(err error) bool {
	return true
}

func (srp *SimpleRetryPolicy) RetryDelay() time.Duration {
	return srp.delay
}

// AtLeastOnceDelivery delivers events with at-least-once semantics.
type AtLeastOnceDelivery struct {
	retryPolicy RetryPolicy
}

func NewAtLeastOnceDelivery(retryPolicy RetryPolicy) *AtLeastOnceDelivery {
	return &AtLeastOnceDelivery{retryPolicy: retryPolicy}
}

func (alod *AtLeastOnceDelivery) Deliver(ctx context.Context, event interface{}, sendFunc func() error) error {
	for {
		err := sendFunc()
		if err == nil {
			return nil
		}
		if !alod.retryPolicy.ShouldRetry(err) {
			return err
		}
		select {
		case <-time.After(alod.retryPolicy.RetryDelay()):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
