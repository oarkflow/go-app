package main

import (
	events "go-app"
)

func main() {
	server := events.NewServer()
	server.Start(":8080")
}
