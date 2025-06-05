package main

import (
	"fmt"
	"log"
	"time"
	
	"github.com/kaptinlin/jsonschema"
	"github.com/oarkflow/json/jsonschema/v2"
)

type User struct {
	UserID    int       `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

var data = map[string]any{
	"user_id": 1,
}
var schemeBytes = []byte(`{
    "type": "object",
    "description": "users",
	"required": ["user_id"],
    "properties": {
        "created_at": {
            "type": ["object", "string"],
            "default": "2024-01-01"
        },
        "user_id": {
            "type": [
                "integer",
                "string"
            ],
            "maxLength": 64
        }
    }
}`)

func main() {
	v2main()
}

func v2main() {
	start := time.Now()
	compiler := v2.NewCompiler()
	schema, err := compiler.Compile(schemeBytes)
	if err != nil {
		log.Fatalf("Failed to compile schema: %v", err)
	}
	v, err1 := schema.SmartUnmarshal(data)
	if err1 != nil {
		fmt.Println(err1, err)
	}
	fmt.Println(v)
	fmt.Println("Time elapsed:", time.Since(start))
}

func kaptlin() {
	start := time.Now()
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile(schemeBytes)
	if err != nil {
		log.Fatalf("Failed to compile schema: %v", err)
	}
	
	// Step 1: Validate
	result := schema.Validate(data)
	if result.IsValid() {
		// Step 2: Unmarshal with defaults
		var user User
		err := schema.Unmarshal(&user, data)
		if err != nil {
			panic(err)
		}
		fmt.Println(user)
	} else {
		log.Println("Validation failed:", result.Errors)
	}
	fmt.Println("Time elapsed:", time.Since(start))
}
