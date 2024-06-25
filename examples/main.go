package main

import (
	"context"
	"log"
	"time"
	
	events "go-app"
)

func main() {
	ctx := context.Background()
	
	storage := events.NewMemoryStorage[string]()
	retryPolicy := events.NewSimpleRetryPolicy(2 * time.Second)
	delivery := events.NewAtLeastOnceDelivery(retryPolicy)
	
	producer := events.NewProducer(
		events.WithStorage[string](storage),
		events.WithRetryPolicy[string](retryPolicy),
		events.WithDeliveryGuarantee[string](delivery),
	)
	
	go producer.Start(ctx)
	
	sub := producer.Subscribe(10)
	go func() {
		for {
			event, err := sub.Next(ctx)
			if err != nil {
				log.Println("Subscription error:", err)
				return
			}
			log.Println("Received event:", event)
		}
	}()
	
	producer.Broadcast(ctx, "Hello, World!")
	time.Sleep(5 * time.Second)
}
