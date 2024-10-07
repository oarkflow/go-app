package dag

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

type Result struct {
	TaskID  string
	NodeKey string
	Payload json.RawMessage
	Status  string
	Error   error
}

type Task struct {
	ID      string
	NodeKey string
	Payload json.RawMessage
	Results map[string]Result
}

func NewTask(nodeKey string, payload json.RawMessage, id ...string) *Task {
	task := &Task{
		NodeKey: nodeKey,
		Payload: payload,
		Results: make(map[string]Result),
	}
	if len(id) > 0 && id[0] != "" {
		task.ID = id[0]
	} else {
		task.ID = NewID()
	}
	return task
}

type TaskManager struct {
	dag   *DAG
	Tasks map[string]*Task
	mu    sync.Mutex
}

func NewTaskManager(dag *DAG) *TaskManager {
	return &TaskManager{
		Tasks: make(map[string]*Task),
		dag:   dag,
	}
}

func (d *TaskManager) AddTask(nodeKey string, payload json.RawMessage, id ...string) *Task {
	d.mu.Lock()
	defer d.mu.Unlock()
	task := NewTask(nodeKey, payload, id...)
	d.Tasks[nodeKey] = task
	return task
}

func (d *TaskManager) GetTask(nodeKey string) (*Task, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	task, ok := d.Tasks[nodeKey]
	return task, ok
}

func (d *TaskManager) processSimpleEdge(ctx context.Context, targets []string, task *Task, result Result) Result {
	for _, targetKey := range targets {
		nextResult := d.processTask(ctx, task.ID, targetKey, result.Payload)
		if nextResult.Error != nil {
			return nextResult
		}
		task.Results[targetKey] = nextResult
		result = nextResult
	}
	return result
}

func (d *TaskManager) processLoopEdge(ctx context.Context, targets []string, task *Task, result Result, nodeKey string) Result {
	var items []json.RawMessage
	if err := json.Unmarshal(result.Payload, &items); err != nil {
		return Result{Error: fmt.Errorf("expected array for LoopEdge: %v", err)}
	}
	var aggregatedResults []json.RawMessage
	for _, item := range items {
		for _, targetKey := range targets {
			individualResult := d.processTask(ctx, task.ID, targetKey, item)
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
	task.Results[nodeKey] = Result{
		TaskID:  task.ID,
		NodeKey: nodeKey,
		Payload: aggregatedJSON,
	}
	result.Payload = aggregatedJSON
	return result
}

func (d *TaskManager) processConditionEdge(ctx context.Context, conditions map[ID]Condition, task *Task, result Result) Result {
	for conditionID, conditionMap := range conditions {
		conditionResult := d.processTask(ctx, task.ID, string(conditionID), result.Payload)
		if conditionResult.Error != nil {
			return conditionResult
		}
		nextNodeKey := conditionMap[conditionResult.Status]
		if nextNodeKey == "" {
			return Result{Error: fmt.Errorf("invalid condition status: %s", conditionResult.Status)}
		}
		nextResult := d.processTask(ctx, task.ID, nextNodeKey, result.Payload)
		if nextResult.Error != nil {
			return nextResult
		}
		task.Results[nextNodeKey] = nextResult
		result = nextResult
	}
	return result
}

func (d *TaskManager) processResult(ctx context.Context, result Result) Result {
	task, ok := d.GetTask(result.NodeKey)
	if task == nil || !ok {
		return Result{Error: fmt.Errorf("task not found: %s", result.TaskID)}
	}
	node, ok := d.dag.Nodes[result.NodeKey]
	if node == nil || !ok {
		return Result{Error: fmt.Errorf("node not found: %s", result.TaskID)}
	}
	task.Results[node.Key] = result
	for _, edge := range d.dag.Edges {
		if edge.Source == node.Key {
			switch edge.EdgeType {
			case SimpleEdge:
				result = d.processSimpleEdge(ctx, edge.Targets, task, result)
			case LoopEdge:
				result = d.processLoopEdge(ctx, edge.Targets, task, result, node.Key)
			case ConditionEdge:
				result = d.processConditionEdge(ctx, edge.Conditions, task, result)
			}
		}
	}
	return result
}

func (d *TaskManager) processTask(ctx context.Context, id string, nodeKey string, payload json.RawMessage) Result {
	task := d.AddTask(nodeKey, payload, id)
	node, ok := d.dag.Nodes[nodeKey]
	if !ok {
		return Result{Error: fmt.Errorf("node %s not found", nodeKey)}
	}
	return d.processNode(ctx, node, task)
}

func (d *TaskManager) processNode(ctx context.Context, node *Node, task *Task) Result {
	result := node.Handler(ctx, task.Payload)
	if result.Error != nil {
		return result
	}
	result.NodeKey = node.Key
	result.TaskID = task.ID
	return d.processResult(ctx, result)
}
