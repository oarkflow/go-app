package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/oarkflow/dag/supervisor/app"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load environment variables from files:
	if err := app.Run(ctx); err != nil {
		slog.Error("Error running app", slog.String("err", err.Error()))
		os.Exit(1)
	}
}
