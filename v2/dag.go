package v2

import (
	"encoding/json"
	"fmt"
	"sync"
)

type Handler func(task *Task) Result

type Result struct {
	TaskID  string          `json:"task_id"`
	NodeKey string          `json:"node_key"`
	Payload json.RawMessage `json:"payload"`
	Status  string          `json:"status"`
	Error   error           `json:"error"`
}

type Task struct {
	ID      string            `json:"id"`
	NodeKey string            `json:"node_key"`
	Payload json.RawMessage   `json:"payload"`
	Results map[string]Result `json:"results"`
}

type Node struct {
	Key     string
	Edges   []Edge
	handler Handler
}

type EdgeType int

const (
	SimpleEdge EdgeType = iota
	LoopEdge
	ConditionEdge
)

type Edge struct {
	From      *Node
	To        *Node
	Type      EdgeType
	Condition func(result Result) bool
}

type TaskManager struct {
	Nodes        map[string]*Node
	wg           sync.WaitGroup
	mutex        sync.Mutex
	finalResults []Result
	done         chan struct{}
}

func NewTaskManager() *TaskManager {
	return &TaskManager{
		Nodes:        make(map[string]*Node),
		finalResults: make([]Result, 0),
		done:         make(chan struct{}),
	}
}

func (tm *TaskManager) AddNode(key string, handler Handler) {
	tm.Nodes[key] = &Node{
		Key:     key,
		handler: handler,
	}
}

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

func (tm *TaskManager) ProcessTask(node string, task *Task) Result {
	startNode, ok := tm.Nodes[node]
	if !ok {
		return Result{Error: fmt.Errorf("node %s not found", node)}
	}
	tm.wg.Add(1)
	go tm.processNode(startNode, task, nil)
	go func() {
		tm.wg.Wait()
		close(tm.done)
	}()
	<-tm.done
	tm.mutex.Lock()
	defer tm.mutex.Unlock()
	if len(tm.finalResults) == 1 {
		return tm.callback(tm.finalResults[0])
	}
	return tm.callback(tm.finalResults)
}

func (tm *TaskManager) callback(results any) Result {
	var rs Result
	switch res := results.(type) {
	case []Result:
		finalUsers := make([]map[string]any, 0)
		for _, result := range res {
			var user map[string]any
			err := json.Unmarshal(result.Payload, &user)
			if err != nil {
				rs.Error = err
				return rs
			}
			finalUsers = append(finalUsers, user)
		}
		finalOutput, err := json.Marshal(finalUsers)
		if err != nil {
			rs.Error = err
			return rs
		}
		rs.Payload = finalOutput
	case Result:
		var user map[string]any
		err := json.Unmarshal(res.Payload, &user)
		if err != nil {
			rs.Error = err
			return rs
		}
		finalOutput, err := json.Marshal(user)
		if err != nil {
			rs.Error = err
			return rs
		}
		rs.Payload = finalOutput
	}
	return rs
}

func (tm *TaskManager) appendResult(result Result) {
	tm.mutex.Lock()
	tm.finalResults = append(tm.finalResults, result)
	tm.mutex.Unlock()
}

func (tm *TaskManager) processNode(node *Node, task *Task, parentNode *Node) {
	defer tm.wg.Done()
	result := node.handler(task)
	tm.mutex.Lock()
	task.Results[node.Key] = result
	tm.mutex.Unlock()
	if len(node.Edges) == 0 {
		if parentNode != nil {
			tm.appendResult(result)
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
				go tm.processNode(edge.To, loopTask, node)
			}
		case ConditionEdge:
			if edge.Condition(result) && edge.To != nil {
				tm.wg.Add(1)
				go tm.processNode(edge.To, task, node)
			} else if parentNode != nil {
				tm.appendResult(result)
			}
		case SimpleEdge:
			if edge.To != nil {
				tm.wg.Add(1)
				go tm.processNode(edge.To, task, node)
			} else if parentNode != nil {
				tm.appendResult(result)
			}
		}
	}
}
