package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
)

func NewClient(address string) (*Client, error) {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server: %v", err)
	}
	return &Client{conn: conn, topics: make(map[string]struct{})}, nil
}

func (c *Client) Start() {
	go c.receiveMessages()
	c.sendMessages()
}

func (c *Client) receiveMessages() {
	scanner := bufio.NewScanner(c.conn)
	for scanner.Scan() {
		var msg Message
		err := json.Unmarshal(scanner.Bytes(), &msg)
		if err != nil {
			log.Printf("Error decoding message: %v", err)
			continue
		}
		fmt.Printf("Received: [%s] %s\n", msg.Topic, msg.Message)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Error reading from server: %v", err)
	}
}

func (c *Client) sendMessages() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		input := scanner.Text()
		var cmd Command
		if _, err := fmt.Sscanf(input, "subscribe:%s", &cmd.Topic); err == nil {
			cmd.Action = "subscribe"
		} else if _, err := fmt.Sscanf(input, "publish:%s:%s", &cmd.Topic, &cmd.Message); err == nil {
			cmd.Action = "publish"
		} else {
			fmt.Println("Invalid command. Use 'subscribe:<topic>' or 'publish:<topic>:<message>'")
			continue
		}
		
		data, err := json.Marshal(cmd)
		if err != nil {
			log.Printf("Error encoding message: %v", err)
			continue
		}
		_, err = c.conn.Write(data)
		_, err = c.conn.Write([]byte("\n"))
		if err != nil {
			log.Printf("Failed to send message: %v", err)
			return
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Error reading from stdin: %v", err)
	}
}

type Command struct {
	Action  string `json:"action"`
	Topic   string `json:"topic"`
	Message string `json:"message,omitempty"`
}

type Message struct {
	Topic   string `json:"topic"`
	Message string `json:"message"`
}
