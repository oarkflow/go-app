package main

import (
	"log"
	
	events "go-app"
)

func main() {
	client, err := events.NewClient("localhost:8080")
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	client.Start()
}
