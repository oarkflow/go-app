package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

type Node struct {
	ID        string
	Address   string
	Conn      net.Conn
	Connected bool
	Alive     bool // Track if node is alive
}

type DAGServer struct {
	Nodes          map[string]*Node
	Edges          map[string][]string
	Tasks          [][]byte   // Hold all tasks in a slice
	taskMutex      sync.Mutex // Mutex to protect access to the Tasks slice
	nodeStatusCond *sync.Cond // Condition variable to signal when all nodes are ready
	mutex          sync.Mutex // Mutex to protect access to the DAGServer structure
	ready          bool       // Flag indicating if the DAG is ready for task processing
	ConnectedNodes int        // Count of connected nodes
}

func NewDAGServer() *DAGServer {
	return &DAGServer{
		Nodes: make(map[string]*Node),
		Edges: make(map[string][]string),
		Tasks: make([][]byte, 0), // Initialize empty task slice
		ready: false,
	}
}

func (s *DAGServer) AddNode(id, address string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.Nodes[id] = &Node{ID: id, Address: address, Alive: false, Connected: false}
}

func (s *DAGServer) AddEdge(from, to string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.Edges[from] = append(s.Edges[from], to)
}

func (s *DAGServer) RegisterNode(conn net.Conn) {
	decoder := json.NewDecoder(conn)
	var node Node
	err := decoder.Decode(&node)
	if err != nil {
		log.Println("Failed to decode node registration:", err)
		return
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	existingNode, exists := s.Nodes[node.ID]
	if exists && !existingNode.Connected {
		existingNode.Conn = conn
		existingNode.Connected = true
		existingNode.Alive = true
		s.ConnectedNodes++
		log.Printf("Node %s connected", node.ID)
	} else {
		s.Nodes[node.ID] = &Node{
			ID:        node.ID,
			Address:   node.Address,
			Conn:      conn,
			Connected: true,
			Alive:     true,
		}
		s.ConnectedNodes++
		log.Printf("Node %s registered and connected", node.ID)
	}

	// Signal the nodeStatusCond in case the nodes are ready
	s.nodeStatusCond.Broadcast()
}

func (s *DAGServer) WaitForNodes() {
	s.nodeStatusCond.L.Lock()
	defer s.nodeStatusCond.L.Unlock()

	for {
		allConnected := true
		s.mutex.Lock()
		for _, node := range s.Nodes {
			if !node.Connected || !node.Alive {
				allConnected = false
				break
			}
		}
		s.mutex.Unlock()

		if allConnected && s.ConnectedNodes > 0 {
			s.ready = true
			log.Println("All nodes are connected and alive. Server is ready to accept tasks.")
			break
		}

		// Wait until nodeStatusCond is signaled indicating a change in node status
		s.nodeStatusCond.Wait()
	}
}

func (s *DAGServer) MonitorNodes() {
	for {
		time.Sleep(10 * time.Second) // Monitor every 10 seconds

		s.mutex.Lock()
		for _, node := range s.Nodes {
			if node.Connected {
				_, err := node.Conn.Write([]byte("ping"))
				if err != nil {
					log.Printf("Node %s is down. Marking as not alive.", node.ID)
					node.Alive = false
					node.Connected = false
					s.ConnectedNodes--
				} else {
					node.Alive = true
				}
			}
		}
		s.mutex.Unlock()

		// Signal the nodeStatusCond if nodes' statuses have changed
		s.nodeStatusCond.Broadcast()
	}
}

func (s *DAGServer) AddTask(task []byte) {
	s.taskMutex.Lock()
	defer s.taskMutex.Unlock()
	s.Tasks = append(s.Tasks, task)

	log.Printf("Task added to queue. Total queued tasks: %d", len(s.Tasks))

	// If the DAG is ready, process tasks immediately
	if s.ready {
		s.nodeStatusCond.Broadcast()
	}
}

func (s *DAGServer) ProcessTasks() {
	for {
		// Wait until all nodes are ready
		s.nodeStatusCond.L.Lock()
		for !s.ready {
			s.nodeStatusCond.Wait()
		}
		s.nodeStatusCond.L.Unlock()

		// Process all queued tasks
		s.taskMutex.Lock()
		tasksToProcess := s.Tasks
		s.Tasks = nil // Clear the task queue
		s.taskMutex.Unlock()

		for _, task := range tasksToProcess {
			go s.processTask(task, "start") // assuming "start" is the initial node
		}
	}
}

func (s *DAGServer) processTask(task []byte, nodeID string) {
	s.mutex.Lock()
	node, exists := s.Nodes[nodeID]
	s.mutex.Unlock()

	if !exists || !node.Connected || !node.Alive {
		log.Printf("Node %s not found, not connected, or not alive", nodeID)
		return
	}

	_, err := node.Conn.Write(task)
	if err != nil {
		log.Printf("Failed to send task to node %s: %v", nodeID, err)
		s.mutex.Lock()
		node.Alive = false
		s.mutex.Unlock()
		return
	}

	buffer := make([]byte, 1024)
	n, err := node.Conn.Read(buffer)
	if err != nil {
		log.Printf("Failed to read response from node %s: %v", nodeID, err)
		s.mutex.Lock()
		node.Alive = false
		s.mutex.Unlock()
		return
	}

	log.Printf("Node %s processed task. Response: %s", nodeID, string(buffer[:n]))

	// Move to next node based on DAG logic
	nextNodes := s.Edges[nodeID]
	for _, nextNode := range nextNodes {
		s.processTask(buffer[:n], nextNode)
	}
}

func (s *DAGServer) ServeAPI() {
	http.HandleFunc("/add-task", func(w http.ResponseWriter, r *http.Request) {
		s.mutex.Lock()
		allNodesAlive := true
		for _, node := range s.Nodes {
			if !node.Alive {
				allNodesAlive = false
				break
			}
		}
		s.mutex.Unlock()

		if !allNodesAlive {
			fmt.Fprintf(w, "Warning: Some nodes are down. Processing might be delayed.\n")
		}

		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusInternalServerError)
			return
		}

		s.AddTask(body)
		fmt.Fprintf(w, "Task added: %s", string(body))
	})

	log.Println("Starting HTTP server on :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func main() {
	dagServer := NewDAGServer()
	dagServer.nodeStatusCond = sync.NewCond(&sync.Mutex{})

	// Add nodes and edges to the DAG server (for example)
	dagServer.AddNode("start", "localhost:8081")
	dagServer.AddNode("middle", "localhost:8082")
	dagServer.AddNode("end", "localhost:8083")
	dagServer.AddEdge("start", "middle")
	dagServer.AddEdge("middle", "end")

	// Start server to listen for nodes
	go func() {
		listener, err := net.Listen("tcp", ":8080")
		if err != nil {
			log.Fatalf("Failed to start DAG server: %v", err)
		}
		defer listener.Close()

		log.Println("DAG server started. Waiting for nodes to connect...")

		for {
			conn, err := listener.Accept()
			if err != nil {
				log.Printf("Failed to accept connection: %v", err)
				continue
			}

			go dagServer.RegisterNode(conn)
		}
	}()

	// Wait for all nodes to be ready
	go dagServer.WaitForNodes()

	// Monitor node health in background
	go dagServer.MonitorNodes()

	// Start processing tasks when nodes are ready
	go dagServer.ProcessTasks()

	// Start HTTP API for task submission
	dagServer.ServeAPI()
}
