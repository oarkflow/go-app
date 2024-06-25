package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
)

type Client struct {
	conn   net.Conn
	topics map[string]struct{}
}

type Server struct {
	mu           sync.Mutex
	clients      map[net.Conn]*Client
	topicClients map[string]map[net.Conn]struct{}
	registerCh   chan *Client
	unregisterCh chan net.Conn
	broadcastCh  chan *Message
}

func NewServer() *Server {
	return &Server{
		clients:      make(map[net.Conn]*Client),
		topicClients: make(map[string]map[net.Conn]struct{}),
		registerCh:   make(chan *Client),
		unregisterCh: make(chan net.Conn),
		broadcastCh:  make(chan *Message),
	}
}

func (s *Server) Start(address string) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
	defer listener.Close()
	
	go s.handleChannels()
	
	fmt.Println("Server started on", address)
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}
		client := &Client{
			conn:   conn,
			topics: make(map[string]struct{}),
		}
		s.registerCh <- client
		go s.handleConnection(client)
	}
}

func (s *Server) handleChannels() {
	for {
		select {
		case client := <-s.registerCh:
			s.mu.Lock()
			s.clients[client.conn] = client
			s.mu.Unlock()
			fmt.Println("New client registered")
		
		case conn := <-s.unregisterCh:
			s.mu.Lock()
			client, ok := s.clients[conn]
			if ok {
				for topic := range client.topics {
					delete(s.topicClients[topic], conn)
				}
				delete(s.clients, conn)
				conn.Close()
				fmt.Println("Client unregistered")
			}
			s.mu.Unlock()
		
		case message := <-s.broadcastCh:
			s.mu.Lock()
			if clients, ok := s.topicClients[message.Topic]; ok {
				for conn := range clients {
					go func(c net.Conn) {
						data, _ := json.Marshal(message)
						_, err := c.Write(data)
						_, err = c.Write([]byte("\n"))
						if err != nil {
							s.unregisterCh <- c
						}
					}(conn)
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *Server) handleConnection(client *Client) {
	scanner := bufio.NewScanner(client.conn)
	for scanner.Scan() {
		var cmd Command
		err := json.Unmarshal(scanner.Bytes(), &cmd)
		if err != nil {
			log.Printf("Error decoding message: %v", err)
			continue
		}
		
		switch cmd.Action {
		case "subscribe":
			s.subscribe(client, cmd.Topic)
		case "publish":
			s.broadcastCh <- &Message{Topic: cmd.Topic, Message: cmd.Message}
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Error reading from connection: %v", err)
	}
	s.unregisterCh <- client.conn
}

func (s *Server) subscribe(client *Client, topic string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	client.topics[topic] = struct{}{}
	if s.topicClients[topic] == nil {
		s.topicClients[topic] = make(map[net.Conn]struct{})
	}
	s.topicClients[topic][client.conn] = struct{}{}
	
	fmt.Printf("Client subscribed to topic: %s\n", topic)
}
