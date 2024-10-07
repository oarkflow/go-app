package dag

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

type EdgeType int

const (
	SimpleEdge EdgeType = iota
	ConditionEdge
	LoopEdge
)

type Handler func(ctx context.Context, payload json.RawMessage) Result

type Node struct {
	Label   string
	Key     string
	Handler Handler
}

type Condition map[string]string

type ID string

type Edge struct {
	Label      string
	Source     string
	Targets    []string
	EdgeType   EdgeType
	Conditions map[ID]Condition
}

type DAG struct {
	Nodes       map[string]*Node
	Edges       []Edge
	taskManager map[string]*TaskManager
	mu          sync.RWMutex
	StartNode   string
}

func New() *DAG {
	return &DAG{
		Nodes:       make(map[string]*Node),
		taskManager: make(map[string]*TaskManager),
	}
}

func (d *DAG) AddNode(key, label string, handler Handler, firstNode ...bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	node := &Node{
		Label:   label,
		Key:     key,
		Handler: handler,
	}
	if len(firstNode) > 0 && firstNode[0] {
		d.StartNode = key
	}
	d.Nodes[key] = node
}

func (d *DAG) AddEdge(label string, edgeType EdgeType, source string, targets []string, conditions ...map[ID]Condition) {
	d.mu.Lock()
	defer d.mu.Unlock()
	edge := Edge{
		Label:    label,
		Source:   source,
		EdgeType: edgeType,
		Targets:  targets,
	}
	if len(conditions) > 0 {
		edge.Conditions = conditions[0]
	}
	d.Edges = append(d.Edges, edge)
}

func (d *DAG) ProcessTask(ctx context.Context, payload json.RawMessage) Result {
	d.mu.Lock()
	defer d.mu.Unlock()
	taskManager := NewTaskManager(d)
	startNodeKey, ok := d.FirstNode()
	if startNodeKey == "" || !ok {
		return Result{Error: fmt.Errorf("no start node found")}
	}
	taskID := NewID()
	d.taskManager[taskID] = taskManager
	return taskManager.processTask(ctx, taskID, startNodeKey, payload)
}

func (d *DAG) FirstNode() (string, bool) {
	if d.StartNode != "" {
		return d.StartNode, true
	}
	inDegree := make(map[string]int)
	for id := range d.Nodes {
		inDegree[id] = 0
	}
	for _, edge := range d.Edges {
		for _, outNode := range edge.Targets {
			inDegree[outNode]++
		}
		if edge.EdgeType == ConditionEdge {
			for id, condition := range edge.Conditions {
				inDegree[string(id)]++
				for _, outNode := range condition {
					inDegree[outNode]++
				}
			}
		}
	}
	for n, count := range inDegree {
		if count == 0 {
			return n, true
		}
	}
	return "", false
}
