package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
)

type Node struct {
	ID      string
	Address string
}

func NodeClient(serverAddress, nodeID string) {
	conn, err := net.Dial("tcp", serverAddress)
	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()

	node := Node{ID: nodeID, Address: serverAddress}
	encoder := json.NewEncoder(conn)
	err = encoder.Encode(&node)
	if err != nil {
		log.Fatalf("Failed to send node registration: %v", err)
	}

	for {
		buffer := make([]byte, 1024)
		n, err := conn.Read(buffer)
		if err != nil {
			log.Printf("Connection closed or error: %v", err)
			return
		}

		// Process the task
		log.Printf("Node %s received task: %s", nodeID, string(buffer[:n]))
		result := []byte(fmt.Sprintf("Processed by node %s", nodeID))

		// Send back the result
		_, err = conn.Write(result)
		if err != nil {
			log.Printf("Failed to send result: %v", err)
		}
	}
}

func main() {
	go NodeClient("localhost:8080", "start")
	go NodeClient("localhost:8080", "middle")
	go NodeClient("localhost:8080", "end")

	select {} // Block forever
}
