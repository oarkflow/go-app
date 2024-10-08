package main

/*
import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Handler defines a function to handle tasks.
type Handler func(ctx context.Context, task *Task) Result

// Result represents the result of a task.
type Result struct {
	TaskID  string          `json:"task_id"`
	NodeKey string          `json:"node_key"`
	Payload json.RawMessage `json:"payload"`
	Status  string          `json:"status"`
	Error   error           `json:"error"`
}

// Task represents a task to be processed by a node.
type Task struct {
	ID      string            `json:"id"`
	NodeKey string            `json:"node_key"`
	Payload json.RawMessage   `json:"payload"`
	Results map[string]Result `json:"results"`
}

// Node represents a node that processes tasks.
type Node struct {
	Key     string
	Edges   []Edge
	handler Handler
	conn    net.Conn // Connection to Task Manager in distributed mode
}

// EdgeType defines the type of edges in the node graph.
type EdgeType int

const (
	SimpleEdge EdgeType = iota
	LoopEdge
	ConditionEdge
)

// Edge represents an edge in the node graph.
type Edge struct {
	From      *Node
	To        *Node
	Type      EdgeType
	Condition func(result Result) bool
}

// Mode defines the mode of operation for TaskManager.
type Mode int

const (
	Standalone Mode = iota
	Distributed
)

// TaskManager manages task processing across nodes.
type TaskManager struct {
	Nodes        map[string]*Node
	wg           sync.WaitGroup
	mutex        sync.Mutex
	finalResults []Result
	done         chan struct{}
	mode         Mode                // New field to handle the mode
	consumers    map[string]net.Conn // Map to store TCP consumer connections in distributed mode
	listener     net.Listener        // For listening to incoming TCP connections
	tasks        map[string]*Task    // Store tasks for retrieval
}

// NewTaskManager creates a new TaskManager in the given mode.
func NewTaskManager(mode Mode) *TaskManager {
	return &TaskManager{
		Nodes:        make(map[string]*Node),
		finalResults: make([]Result, 0),
		done:         make(chan struct{}),
		mode:         mode, // Initialize mode
		consumers:    make(map[string]net.Conn),
		tasks:        make(map[string]*Task),
	}
}

// AddNode adds a node to the TaskManager.
func (tm *TaskManager) AddNode(key string, handler Handler) {
	tm.Nodes[key] = &Node{
		Key:     key,
		handler: handler,
	}
}

// AddEdge adds an edge between nodes in the TaskManager.
func (tm *TaskManager) AddEdge(from, to string, edgeType EdgeType) {
	fromNode, ok := tm.Nodes[from]
	if !ok {
		return
	}
	toNode, ok := tm.Nodes[to]
	if !ok {
		return
	}
	fromNode.Edges = append(fromNode.Edges, Edge{
		From: fromNode,
		To:   toNode,
		Type: edgeType,
	})
}

// StartServer starts the TaskManager as a server in Distributed mode.
func (tm *TaskManager) StartServer(address string) error {
	if tm.mode != Distributed {
		return fmt.Errorf("task manager is not in distributed mode")
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to start server: %v", err)
	}
	tm.listener = listener
	fmt.Println("Task manager listening on", address)

	go tm.acceptConsumers() // Accept incoming consumer connections
	return nil
}

// acceptConsumers accepts incoming consumer connections.
func (tm *TaskManager) acceptConsumers() {
	for {
		conn, err := tm.listener.Accept()
		if err != nil {
			fmt.Println("failed to accept connection:", err)
			continue
		}
		go tm.handleConsumer(conn) // Handle each consumer in a separate goroutine
	}
}

// handleConsumer handles communication with a consumer node.
func (tm *TaskManager) handleConsumer(conn net.Conn) {
	defer conn.Close()
	consumerID := conn.RemoteAddr().String()

	// Register the consumer
	tm.mutex.Lock()
	tm.consumers[consumerID] = conn
	tm.mutex.Unlock()

	fmt.Println("Consumer connected:", consumerID)

	for {
		// Listen for the result sent back from the consumer
		result := tm.receiveResult(conn)
		if result != nil {
			tm.appendFinalResult(*result)
			fmt.Println("Result received for Task ID:", result.TaskID)
		}
	}
}

// receiveResult receives task results from a consumer.
func (tm *TaskManager) receiveResult(conn net.Conn) *Result {
	var result Result
	decoder := json.NewDecoder(conn)
	if err := decoder.Decode(&result); err != nil {
		fmt.Println("failed to receive result:", err)
		return nil
	}
	return &result
}

// processTask processes a task either locally (Standalone mode) or by forwarding it to consumers (Distributed mode).
func (tm *TaskManager) processTask(ctx context.Context, node string, task *Task) Result {
	if tm.mode == Distributed {
		// Forward task to an available connected consumer
		tm.mutex.Lock()
		for _, conn := range tm.consumers {
			go tm.sendTaskToConsumer(conn, task)
		}
		tm.mutex.Unlock()

		// Return Task ID instantly
		return Result{TaskID: task.ID, Status: "Task submitted"}
	}

	// Standalone mode (same as the existing implementation)
	return tm.processStandaloneTask(ctx, node, task)
}

// sendTaskToConsumer sends a task to a consumer node.
func (tm *TaskManager) sendTaskToConsumer(conn net.Conn, task *Task) {
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(task); err != nil {
		fmt.Println("failed to send task to consumer:", err)
	}
}

// processStandaloneTask processes a task locally (Standalone mode).
func (tm *TaskManager) processStandaloneTask(ctx context.Context, node string, task *Task) Result {
	startNode, ok := tm.Nodes[node]
	if !ok {
		return Result{Error: fmt.Errorf("node %s not found", node)}
	}
	tm.wg.Add(1)
	go tm.processNode(ctx, startNode, task, nil)
	go func() {
		tm.wg.Wait()
		close(tm.done)
	}()
	select {
	case <-ctx.Done():
		return Result{Error: ctx.Err()}
	case <-tm.done:
		tm.mutex.Lock()
		defer tm.mutex.Unlock()
		if len(tm.finalResults) == 1 {
			return tm.callback(tm.finalResults[0])
		}
		return tm.callback(tm.finalResults)
	}
}

// callback aggregates task results.
func (tm *TaskManager) callback(results any) Result {
	var rs Result
	switch res := results.(type) {
	case []Result:
		aggregatedOutput := make([]json.RawMessage, 0)
		for _, result := range res {
			var item json.RawMessage
			err := json.Unmarshal(result.Payload, &item)
			if err != nil {
				rs.Error = err
				return rs
			}
			aggregatedOutput = append(aggregatedOutput, item)
		}
		finalOutput, err := json.Marshal(aggregatedOutput)
		if err != nil {
			rs.Error = err
			return rs
		}
		rs.Payload = finalOutput
	case Result:
		var item json.RawMessage
		err := json.Unmarshal(res.Payload, &item)
		if err != nil {
			rs.Error = err
			return rs
		}
		finalOutput, err := json.Marshal(item)
		if err != nil {
			rs.Error = err
			return rs
		}
		rs.Payload = finalOutput
	}
	return rs
}

// appendFinalResult appends a result to the finalResults list.
func (tm *TaskManager) appendFinalResult(result Result) {
	tm.mutex.Lock()
	tm.finalResults = append(tm.finalResults, result)
	tm.mutex.Unlock()
}

// processNode processes a node in the task manager.
func (tm *TaskManager) processNode(ctx context.Context, node *Node, task *Task, parentNode *Node) {
	defer tm.wg.Done()
	var result Result
	select {
	case <-ctx.Done():
		result = Result{TaskID: task.ID, NodeKey: node.Key, Error: ctx.Err()}
		tm.appendFinalResult(result)
		return
	default:
		result = node.handler(ctx, task)
	}
	tm.mutex.Lock()
	task.Results[node.Key] = result
	tm.mutex.Unlock()
	if len(node.Edges) == 0 {
		if parentNode != nil {
			tm.appendFinalResult(result)
		}
		return
	}
	for _, edge := range node.Edges {
		switch edge.Type {
		case LoopEdge:
			var items []json.RawMessage
			json.Unmarshal(task.Payload, &items)
			for _, item := range items {
				loopTask := &Task{
					ID:      task.ID,
					NodeKey: edge.From.Key,
					Payload: item,
					Results: task.Results,
				}
				tm.wg.Add(1)
				go tm.processNode(ctx, edge.To, loopTask, node)
			}
		case ConditionEdge:
			if edge.Condition(result) && edge.To != nil {
				tm.wg.Add(1)
				go tm.processNode(ctx, edge.To, task, node)
			} else if parentNode != nil {
				tm.appendFinalResult(result)
			}
		case SimpleEdge:
			if edge.To != nil {
				tm.wg.Add(1)
				go tm.processNode(ctx, edge.To, task, node)
			} else if parentNode != nil {
				tm.appendFinalResult(result)
			}
		}
	}
}

// Example task handlers
func handler1(ctx context.Context, task *Task) Result {
	fmt.Println("Processing task at handler1")
	time.Sleep(1 * time.Second) // Simulate work
	return Result{
		TaskID:  task.ID,
		NodeKey: "handler1",
		Payload: json.RawMessage(`{"data": "result1"}`),
		Status:  "completed",
	}
}

func handler2(ctx context.Context, task *Task) Result {
	fmt.Println("Processing task at handler2")
	time.Sleep(2 * time.Second) // Simulate work
	return Result{
		TaskID:  task.ID,
		NodeKey: "handler2",
		Payload: json.RawMessage(`{"data": "result2"}`),
		Status:  "completed",
	}
}

// HTTP API for Task Manager
func main() {
	// Create Task Manager in standalone mode for now (switch to Distributed for TCP-based processing)
	tm := NewTaskManager(Standalone)

	// Add Nodes and Handlers
	tm.AddNode("node1", handler1)
	tm.AddNode("node2", handler2)

	// Add Edges between nodes (simple sequential flow)
	tm.AddEdge("node1", "node2", SimpleEdge)

	// HTTP API to publish and request tasks
	http.HandleFunc("/publish", func(w http.ResponseWriter, r *http.Request) {
		var task Task
		if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
			http.Error(w, "Invalid task format", http.StatusBadRequest)
			return
		}

		// Generate a new Task ID and store the task
		task.ID = uuid.NewString()
		tm.mutex.Lock()
		tm.tasks[task.ID] = &task
		tm.mutex.Unlock()

		// Process the task
		go tm.processTask(r.Context(), task.NodeKey, &task)

		// Respond with Task ID
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"task_id": task.ID})
	})

	http.HandleFunc("/request", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			TaskID string `json:"task_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}

		// Retrieve the task result
		tm.mutex.Lock()
		task, ok := tm.tasks[request.TaskID]
		tm.mutex.Unlock()
		if !ok {
			http.Error(w, "Task not found", http.StatusNotFound)
			return
		}

		// Return the task results
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(task.Results)
	})

	fmt.Println("API server started on :8080")
	http.ListenAndServe(":8080", nil)
}
*/
