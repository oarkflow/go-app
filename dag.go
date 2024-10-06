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
	startNode   *Node
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
		d.startNode = node
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
	startNode := d.getStartNode()
	if startNode == nil {
		return Result{Error: fmt.Errorf("no start node found")}
	}
	taskID := NewID()
	d.taskManager[taskID] = taskManager
	return taskManager.processTask(ctx, taskID, startNode.Key, payload)
}

func (d *DAG) getStartNode() *Node {
	if d.startNode != nil {
		return d.startNode
	}
	for _, node := range d.Nodes {
		if d.isStartNode(node.Key) {
			return node
		}
	}
	return nil
}

func (d *DAG) isStartNode(nodeKey string) bool {
	for _, edge := range d.Edges {
		for _, target := range edge.Targets {
			if target == nodeKey {
				return false
			}
		}
	}
	return true
}
