package dag

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

type Result struct {
	Payload json.RawMessage
	Status  string
	Error   error
}

type Task struct {
	NodeKey string
	Payload json.RawMessage
	Results map[string]json.RawMessage
}

type TaskManager struct {
	Tasks map[string]*Task
	mu    sync.Mutex
}

func NewTaskManager() *TaskManager {
	return &TaskManager{
		Tasks: make(map[string]*Task),
	}
}

func (tm *TaskManager) AddTask(nodeKey string, payload json.RawMessage) *Task {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	task := &Task{
		NodeKey: nodeKey,
		Payload: payload,
		Results: make(map[string]json.RawMessage),
	}
	tm.Tasks[nodeKey] = task
	return task
}

func (tm *TaskManager) GetTask(nodeKey string) *Task {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.Tasks[nodeKey]
}

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
	Nodes     map[string]Node
	Edges     []Edge
	mu        sync.RWMutex
	startNode *Node
}

func New() *DAG {
	return &DAG{
		Nodes: make(map[string]Node),
	}
}

func (d *DAG) AddNode(key, label string, handler Handler, firstNode ...bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	node := Node{
		Label:   label,
		Key:     key,
		Handler: handler,
	}
	if len(firstNode) > 0 && firstNode[0] {
		d.startNode = &node
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
	taskManager := NewTaskManager()
	startNode := d.getStartNode()
	if startNode == nil {
		return Result{Error: fmt.Errorf("no start node found")}
	}
	task := taskManager.AddTask(startNode.Key, payload)
	return d.processNode(ctx, startNode, taskManager, task)
}

func (d *DAG) getStartNode() *Node {
	if d.startNode != nil {
		return d.startNode
	}
	for _, node := range d.Nodes {
		if d.isStartNode(node.Key) {
			return &node
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

func (d *DAG) processNode(ctx context.Context, node *Node, taskManager *TaskManager, task *Task) Result {
	result := node.Handler(ctx, task.Payload)
	if result.Error != nil {
		return result
	}
	task.Results[node.Key] = result.Payload
	for _, edge := range d.Edges {
		if edge.Source == node.Key {
			switch edge.EdgeType {
			case SimpleEdge:
				for _, targetKey := range edge.Targets {
					targetNode := d.Nodes[targetKey]
					nextResult := d.processNode(ctx, &targetNode, taskManager, taskManager.AddTask(targetKey, result.Payload))
					if nextResult.Error != nil {
						return nextResult
					}
					task.Results[targetKey] = nextResult.Payload
					result = nextResult
				}
			case LoopEdge:
				var items []json.RawMessage
				if err := json.Unmarshal(result.Payload, &items); err != nil {
					return Result{Error: fmt.Errorf("expected array for LoopEdge: %v", err)}
				}
				var aggregatedResults []json.RawMessage
				for _, item := range items {
					for _, targetKey := range edge.Targets {
						targetNode := d.Nodes[targetKey]
						individualResult := d.processNode(ctx, &targetNode, taskManager, taskManager.AddTask(targetKey, item))
						if individualResult.Error != nil {
							return individualResult
						}
						aggregatedResults = append(aggregatedResults, individualResult.Payload)
					}
				}
				aggregatedJSON, err := json.Marshal(aggregatedResults)
				if err != nil {
					return Result{Error: err}
				}
				task.Results[node.Key] = aggregatedJSON
				result.Payload = aggregatedJSON
			case ConditionEdge:
				for conditionID, conditionMap := range edge.Conditions {
					conditionNode := d.Nodes[string(conditionID)]
					conditionResult := d.processNode(ctx, &conditionNode, taskManager, taskManager.AddTask(string(conditionID), result.Payload))
					if conditionResult.Error != nil {
						return conditionResult
					}
					nextNodeKey := conditionMap[conditionResult.Status]
					if nextNodeKey == "" {
						return Result{Error: fmt.Errorf("invalid condition status: %s", conditionResult.Status)}
					}
					nextNode := d.Nodes[nextNodeKey]
					nextResult := d.processNode(ctx, &nextNode, taskManager, taskManager.AddTask(nextNodeKey, result.Payload))
					if nextResult.Error != nil {
						return nextResult
					}
					task.Results[nextNodeKey] = nextResult.Payload
					result = nextResult
				}
			}
		}
	}
	return result
}
