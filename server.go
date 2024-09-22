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

type Task struct {
	ID          string          `json:"id"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"created_at"`
	ProcessedAt *time.Time      `json:"processed_at,omitempty"`
	CurrentNode string          `json:"current_node"`
	Error       string          `json:"error,omitempty"`
}

type Node struct {
	ID        string
	Address   string
	Conn      net.Conn
	Connected bool
	Alive     bool
}

type DAGServer struct {
	Nodes          map[string]*Node
	Edges          map[string][]string
	Tasks          []Task
	taskMutex      sync.Mutex
	processedTasks map[string]struct{}
	nodeStatusCond *sync.Cond
	mutex          sync.Mutex
	ready          bool
	ConnectedNodes int
}

func NewDAGServer() *DAGServer {
	return &DAGServer{
		Nodes:          make(map[string]*Node),
		Edges:          make(map[string][]string),
		Tasks:          make([]Task, 0),
		processedTasks: make(map[string]struct{}),
		ready:          false,
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

		s.nodeStatusCond.Wait()
	}
}

func (s *DAGServer) MonitorNodes() {
	for {
		time.Sleep(10 * time.Second)

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

		s.nodeStatusCond.Broadcast()
	}
}

func (s *DAGServer) AddTask(task Task) {
	s.taskMutex.Lock()
	defer s.taskMutex.Unlock()

	task.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	task.CreatedAt = time.Now()
	task.CurrentNode = "start"

	if _, exists := s.processedTasks[task.ID]; exists {
		log.Printf("Duplicate task ID detected: %s. Task will not be added.", task.ID)
		return
	}

	s.Tasks = append(s.Tasks, task)
	s.processedTasks[task.ID] = struct{}{}

	log.Printf("Task %s added to queue. Total queued tasks: %d", task.ID, len(s.Tasks))

	if s.ready {
		s.nodeStatusCond.Broadcast()
	}
}

func (s *DAGServer) ProcessTasks() {
	for {

		s.nodeStatusCond.L.Lock()
		for !s.ready {
			s.nodeStatusCond.Wait()
		}
		s.nodeStatusCond.L.Unlock()

		s.taskMutex.Lock()
		tasksToProcess := s.Tasks
		s.Tasks = nil
		s.taskMutex.Unlock()

		for _, task := range tasksToProcess {
			go s.processTask(task, "start")
		}
	}
}

func (s *DAGServer) processTask(task Task, nodeID string) {
	for {
		s.mutex.Lock()
		node, exists := s.Nodes[nodeID]
		s.mutex.Unlock()

		if !exists {
			log.Printf("Node %s not found. Task %s cannot be processed.", nodeID, task.ID)
			return
		}

		if node.Connected && node.Alive {
			_, err := node.Conn.Write(task.Payload)
			if err != nil {
				log.Printf("Failed to send task %s to node %s: %v", task.ID, nodeID, err)
				s.mutex.Lock()
				node.Alive = false
				s.mutex.Unlock()
				return
			}

			buffer := make([]byte, 1024)
			n, err := node.Conn.Read(buffer)
			if err != nil {
				log.Printf("Failed to read response for task %s from node %s: %v", task.ID, nodeID, err)
				s.mutex.Lock()
				node.Alive = false
				s.mutex.Unlock()
				return
			}

			processedTime := time.Now()
			task.ProcessedAt = &processedTime
			task.CurrentNode = nodeID
			log.Printf("Node %s processed task %s. Response: %s", nodeID, task.ID, string(buffer[:n]))

			nextNodes := s.Edges[nodeID]
			for _, nextNode := range nextNodes {

				newTask := Task{
					ID:          task.ID,
					Payload:     buffer[:n],
					CreatedAt:   task.CreatedAt,
					ProcessedAt: nil,
					CurrentNode: nextNode,
				}
				s.processTask(newTask, nextNode)
			}
			return
		} else {
			log.Printf("Node %s not alive or not connected. Deferring task %s", nodeID, task.ID)
			time.Sleep(5 * time.Second)
		}
	}
}

func (s *DAGServer) ServeAPI() {
	http.HandleFunc("/add-task", func(w http.ResponseWriter, r *http.Request) {
		var task Task
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusInternalServerError)
			return
		}

		err = json.Unmarshal(body, &task)
		if err != nil {
			http.Error(w, "Invalid task format", http.StatusBadRequest)
			return
		}

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

		s.AddTask(task)
		fmt.Fprintf(w, "Task added: %s", task.ID)
	})

	log.Println("Starting HTTP server on :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func main() {
	dagServer := NewDAGServer()
	dagServer.nodeStatusCond = sync.NewCond(&sync.Mutex{})

	dagServer.AddNode("start", "localhost:8081")
	dagServer.AddNode("middle", "localhost:8082")
	dagServer.AddNode("end", "localhost:8083")
	dagServer.AddEdge("start", "middle")
	dagServer.AddEdge("middle", "end")

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
	go dagServer.WaitForNodes()
	go dagServer.MonitorNodes()
	go dagServer.ProcessTasks()
	dagServer.ServeAPI()
}
