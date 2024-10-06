package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oarkflow/dag"
)

func Node1(ctx context.Context, payload json.RawMessage) dag.Result {
	return dag.Result{Payload: payload}
}

func Node2(ctx context.Context, payload json.RawMessage) dag.Result {
	return dag.Result{Payload: payload}
}

func Node3(ctx context.Context, payload json.RawMessage) dag.Result {
	var data map[string]any
	err := json.Unmarshal(payload, &data)
	if err != nil {
		return dag.Result{Error: err}
	}
	data["added"] = "added here"
	bt, _ := json.Marshal(data)
	return dag.Result{Payload: bt}
}

func Node4(ctx context.Context, payload json.RawMessage) dag.Result {
	var data []map[string]any
	err := json.Unmarshal(payload, &data)
	if err != nil {
		return dag.Result{Error: err}
	}
	bt, _ := json.Marshal(map[string]any{"storage": data})
	return dag.Result{Payload: bt}
}

func Node5(ctx context.Context, payload json.RawMessage) dag.Result {
	var data map[string]any
	err := json.Unmarshal(payload, &data)
	if err != nil {
		return dag.Result{Error: err}
	}
	data["added_more"] = "added more here"
	bt, _ := json.Marshal(data)
	return dag.Result{Payload: bt}
}

func CheckCondition(ctx context.Context, payload json.RawMessage) dag.Result {
	var data map[string]any
	err := json.Unmarshal(payload, &data)
	if err != nil {
		return dag.Result{Error: err}
	}
	var status string
	if len(data) == 1 {
		status = "pass"
	} else {
		status = "fail"
	}
	return dag.Result{Payload: payload, Status: status}
}

func Pass(ctx context.Context, payload json.RawMessage) dag.Result {
	fmt.Println("Pass")
	return dag.Result{Payload: payload}
}

func Fail(ctx context.Context, payload json.RawMessage) dag.Result {
	fmt.Println("Fail")
	return dag.Result{Payload: []byte(`{"test2": "asdsa"}`)}
}

func main() {
	flow := dag.New()
	flow.AddNode("node1", "Node 1", Node1, true)
	flow.AddNode("node2", "Node 2", Node2)
	flow.AddNode("node3", "Node 3", Node3)
	flow.AddNode("node4", "Node 4", Node4)
	flow.AddNode("node5", "Node 5", Node5)
	flow.AddNode("condition", "Condition", CheckCondition)
	flow.AddNode("pass-node", "Pass Node", Pass)
	flow.AddNode("fail-node", "Fail Node", Fail)
	flow.AddEdge("Edge 1", dag.SimpleEdge, "node1", []string{"node2"})
	flow.AddEdge("Edge 2", dag.LoopEdge, "node2", []string{"node3"})
	flow.AddEdge("Edge 3", dag.SimpleEdge, "node2", []string{"node4"})
	flow.AddEdge("Edge 4", dag.SimpleEdge, "node3", []string{"node5"})
	flow.AddEdge("Edge 5", dag.ConditionEdge, "node4", nil, map[dag.ID]dag.Condition{"condition": {"pass": "pass-node", "fail": "fail-node"}})
	ctx := context.Background()
	payload := json.RawMessage(`[{"test": 445},{"test": 222}]`)
	result := flow.ProcessTask(ctx, payload)
	if result.Error != nil {
		fmt.Println("Error processing task:", result.Error)
	} else {
		fmt.Println("Final result:", string(result.Payload))
	}
}
