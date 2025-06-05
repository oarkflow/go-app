// app.go

package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/joho/godotenv"
)

func main() {
	// Supervisor sets GOENV to point at the chosen .env file
	envPath := os.Getenv("GOENV")
	if envPath == "" {
		envPath = ".env"
	}
	if err := godotenv.Load(envPath); err != nil {
		slog.Warn("Could not load .env", slog.String("path", envPath), slog.String("err", err.Error()))
	}

	app := fiber.New()
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Fiber app is running!")
	})
	app.Get("/readyz", func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	errCh := make(chan error, 1)
	go func() {
		port := os.Getenv("PORT")
		if port == "" {
			port = "3000"
		}
		slog.Info("Starting Fiber server", slog.String("port", port))
		errCh <- app.Listen(":" + port)
	}()

	// Wait for SIGINT/SIGTERM
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("Graceful shutdown signaled")
		cancel()
	}()

	<-ctx.Done()
	slog.Info("Shutting down Fiber server...")
	_ = app.Shutdown()
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			slog.Error("Fiber server error", slog.String("err", err.Error()))
			os.Exit(1)
		}
	case <-time.After(5 * time.Second):
		slog.Warn("Fiber shutdown timed out")
	}
}
