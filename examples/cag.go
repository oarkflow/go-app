package main

import (
	"context"
	"encoding/json"
	"fmt"

	v2 "github.com/oarkflow/dag/v2"
)

func handler1(ctx context.Context, task *v2.Task) v2.Result {
	return v2.Result{
		TaskID:  task.ID,
		NodeKey: "A",
		Payload: task.Payload,
		Status:  "success",
	}
}

func handler2(ctx context.Context, task *v2.Task) v2.Result {
	var user map[string]any
	json.Unmarshal(task.Payload, &user)
	return v2.Result{
		TaskID:  task.ID,
		NodeKey: "B",
		Payload: task.Payload,
		Status:  "success",
	}
}

func handler3(ctx context.Context, task *v2.Task) v2.Result {
	var user map[string]any
	json.Unmarshal(task.Payload, &user)
	age := int(user["age"].(float64))
	status := "FAIL"
	if age > 20 {
		status = "PASS"
	}
	user["status"] = status
	resultPayload, _ := json.Marshal(user)
	return v2.Result{
		TaskID:  task.ID,
		NodeKey: "C",
		Payload: resultPayload,
		Status:  status,
	}
}

func main() {
	taskManager := v2.NewTaskManager()
	taskManager.AddNode("A", handler1)
	taskManager.AddNode("B", handler2)
	taskManager.AddNode("C", handler3)
	taskManager.AddEdge("A", "B", v2.LoopEdge)
	taskManager.AddEdge("B", "C", v2.SimpleEdge)
	initialPayload, _ := json.Marshal([]map[string]any{
		{"user_id": 1, "age": 12},
		{"user_id": 2, "age": 34},
	})
	task := &v2.Task{
		ID:      "Task1",
		NodeKey: "A",
		Payload: initialPayload,
		Results: make(map[string]v2.Result),
	}
	rs := taskManager.ProcessTask(context.Background(), "A", task)
	fmt.Println(string(rs.Payload))
}
