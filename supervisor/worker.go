package main

import (
	"github.com/oarkflow/dag/supervisor/app"
	"github.com/oarkflow/dag/supervisor/cmd"
)

func main() {
	cmd.Run(app.Run)
}
