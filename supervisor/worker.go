package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"log/slog"

	"github.com/fsnotify/fsnotify"
	"github.com/gofiber/fiber/v2"
	"github.com/joho/godotenv"
	"github.com/natefinch/lumberjack"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ─────────────────────────────────────────────────────────────────────────────
// Configuration
// ─────────────────────────────────────────────────────────────────────────────

const (
	envPath          = ".env"
	graceTimeout     = 5 * time.Second
	debounceDelay    = 500 * time.Millisecond
	pidFile          = "/tmp/supervisor.pid"
	healthPort       = "9999" // Metrics & health listen port
	logFilePath      = "./log/myapp/supervisor.log"
	childLogFilePath = "./log/myapp/child.log"
	restartBackoff   = 1 * time.Second
	maxBackoff       = 30 * time.Second

	// Crash-loop threshold: if more than this many crashes in the window, exit supervisor
	crashLoopThreshold = 5
	crashLoopWindow    = 1 * time.Minute
)

var (
	// Prometheus metrics
	restartCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "supervisor_restart_total",
			Help: "Total number of times the supervisor restarted the child.",
		},
		[]string{"reason"},
	)
	crashCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "supervisor_child_crash_total",
			Help: "Total number of times the child has crashed.",
		},
	)
	uptimeGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "supervisor_uptime_seconds",
			Help: "Supervisor uptime in seconds.",
		},
	)
)

func init() {
	// Register Prometheus metrics
	prometheus.MustRegister(restartCounter, crashCounter, uptimeGauge)
}

// ─────────────────────────────────────────────────────────────────────────────
// Supervisor Struct
// ─────────────────────────────────────────────────────────────────────────────

type Supervisor struct {
	cmdLock        sync.Mutex
	currentCmd     *exec.Cmd
	restartEvent   chan string
	done           chan struct{}
	backoff        time.Duration
	watcher        *fsnotify.Watcher
	crashTimes     []time.Time
	crashTimesLock sync.Mutex
	startTime      time.Time
}

func main() {
	// If RUN_AS_CHILD=1 is set, act as the child worker
	if os.Getenv("RUN_AS_CHILD") == "1" {
		runWorker()
		return
	}

	// Supervisor mode
	setupLogging()
	checkOrCreatePIDFile()
	defer removePIDFile()

	slog.Info("Supervisor starting...")

	s := &Supervisor{
		restartEvent: make(chan string, 1),
		done:         make(chan struct{}),
		backoff:      restartBackoff,
		startTime:    time.Now(),
	}

	s.initWatcher()
	defer s.watcher.Close()

	// Begin watching .env and binary, and spawn monitor loop
	go s.watchFiles()
	go s.spawnAndMonitor()
	go s.startMetricsServer()

	// Handle signals: SIGHUP=reload, SIGINT/SIGTERM=shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				slog.Info("SIGHUP received: scheduling reload")
				s.queueRestart("sighup")
			default:
				slog.Info("Shutdown signal received: tearing down child and exiting")
				close(s.done)
				s.killChild()
				return
			}
		case <-s.done:
			return
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Logging (stdout + rotating file)
// ─────────────────────────────────────────────────────────────────────────────

func setupLogging() {
	// Ensure log directory exists
	if err := os.MkdirAll(filepath.Dir(logFilePath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Could not create log dir: %v\n", err)
		os.Exit(1)
	}

	// Rotating logger for supervisor
	fileLogger := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    10, // megabytes
		MaxBackups: 5,  // number of old log files to keep
		MaxAge:     28, // days
		Compress:   true,
	}

	// Composite: write to both stdout and file
	mw := io.MultiWriter(os.Stdout, fileLogger)
	handler := slog.NewTextHandler(mw, &slog.HandlerOptions{AddSource: false})
	slog.SetDefault(slog.New(handler))
}

// ─────────────────────────────────────────────────────────────────────────────
// PID File Locking
// ─────────────────────────────────────────────────────────────────────────────

func checkOrCreatePIDFile() {
	if _, err := os.Stat(pidFile); err == nil {
		slog.Error("PID file already exists; another instance may be running", slog.String("file", pidFile))
		os.Exit(1)
	}
	pid := os.Getpid()
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", pid)), 0o644); err != nil {
		slog.Error("Failed to write PID file", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func removePIDFile() {
	_ = os.Remove(pidFile)
}

// ─────────────────────────────────────────────────────────────────────────────
// File Watching (.env + binary)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Supervisor) initWatcher() {
	var err error
	s.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		slog.Error("Failed to create fsnotify watcher", slog.String("err", err.Error()))
		os.Exit(1)
	}

	bin, _ := os.Executable()
	toWatch := []string{envPath, bin}
	for _, file := range toWatch {
		abs, _ := filepath.Abs(file)
		dir := filepath.Dir(abs)
		if err := s.watcher.Add(dir); err != nil {
			slog.Error("Failed to add watcher", slog.String("file", file), slog.String("err", err.Error()))
			os.Exit(1)
		}
		slog.Info("Watching for changes", slog.String("path", abs))
	}
}

func (s *Supervisor) watchFiles() {
	timer := time.NewTimer(0)
	<-timer.C // drain immediately

	var mu sync.Mutex
	debounce := func(reason string) {
		mu.Lock()
		defer mu.Unlock()
		timer.Reset(debounceDelay)
		go func() {
			<-timer.C
			s.queueRestart(reason)
		}()
	}

	for {
		select {
		case event := <-s.watcher.Events:
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
				slog.Info("File event detected", slog.String("file", event.Name), slog.String("op", event.Op.String()))
				debounce(event.Name)
			}
		case err := <-s.watcher.Errors:
			slog.Error("Watcher error", slog.String("err", err.Error()))
		case <-s.done:
			return
		}
	}
}

func (s *Supervisor) queueRestart(reason string) {
	select {
	case s.restartEvent <- reason:
	default:
		// Already queued; do nothing
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Child Process Lifecycle (spawn, monitor, kill, backoff, crash-loop guard)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Supervisor) spawnAndMonitor() {
	for {
		select {
		case <-s.done:
			return
		default:
		}

		s.resetBackoff()
		if err := s.startChild(); err != nil {
			slog.Error("Failed to start child process", slog.String("err", err.Error()))
			os.Exit(1)
		}

		exitCh := make(chan error, 1)
		go func() {
			exitCh <- s.currentCmd.Wait()
		}()

		select {
		case reason := <-s.restartEvent:
			slog.Info("Supervisor restarting child", slog.String("reason", reason))
			restartCounter.WithLabelValues(reason).Inc()
			s.killChild()
			continue
		case err := <-exitCh:
			// Child exited (crash or clean). Check crash-loop
			if err != nil {
				slog.Error("Child exited with error", slog.String("err", err.Error()))
				crashCounter.Inc()
				if !s.recordCrashAndCheckLoop() {
					slog.Error("Too many child crashes in short time; exiting supervisor")
					os.Exit(1)
				}
			} else {
				slog.Info("Child exited cleanly")
			}
			// Backoff before restart
			if s.backoffAndSleep() {
				continue
			}
			return
		case <-s.done:
			s.killChild()
			return
		}
	}
}

func (s *Supervisor) startChild() error {
	s.cmdLock.Lock()
	defer s.cmdLock.Unlock()

	bin, _ := os.Executable()
	cmd := exec.Command(bin, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "RUN_AS_CHILD=1")

	// Child logs to a rotating file + stdout
	if err := os.MkdirAll(filepath.Dir(childLogFilePath), 0o755); err != nil {
		slog.Error("Failed to create child log dir", slog.String("err", err.Error()))
		return err
	}
	childFileLogger := &lumberjack.Logger{
		Filename:   childLogFilePath,
		MaxSize:    10, // MB
		MaxBackups: 5,
		MaxAge:     28, // days
		Compress:   true,
	}
	cmd.Stdout = io.MultiWriter(os.Stdout, childFileLogger)
	cmd.Stderr = io.MultiWriter(os.Stderr, childFileLogger)

	// Put child in its own process group for isolation
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return err
	}
	s.currentCmd = cmd
	slog.Info("Spawned child process", slog.Int("pid", cmd.Process.Pid))
	return nil
}

func (s *Supervisor) killChild() {
	s.cmdLock.Lock()
	defer s.cmdLock.Unlock()

	if s.currentCmd == nil {
		return
	}

	proc := s.currentCmd.Process
	_ = proc.Signal(syscall.SIGTERM) // Graceful
	done := make(chan struct{})

	go func() {
		s.currentCmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("Child terminated gracefully")
	case <-time.After(graceTimeout):
		slog.Warn("Child did not exit in time; sending SIGKILL")
		_ = proc.Kill()
		<-done
	}
	s.currentCmd = nil
}

func (s *Supervisor) resetBackoff() {
	s.backoff = restartBackoff
}

func (s *Supervisor) backoffAndSleep() bool {
	d := s.backoff
	if s.backoff < maxBackoff {
		s.backoff *= 2
	}
	slog.Info("Supervisor backing off before restart", slog.Duration("duration", d))
	select {
	case <-time.After(d):
		return true
	case <-s.done:
		return false
	}
}

// recordCrashAndCheckLoop keeps track of recent crash timestamps and returns false
// if the crash count exceeds crashLoopThreshold within crashLoopWindow.
func (s *Supervisor) recordCrashAndCheckLoop() bool {
	s.crashTimesLock.Lock()
	defer s.crashTimesLock.Unlock()

	now := time.Now()
	windowStart := now.Add(-crashLoopWindow)

	// Purge old timestamps
	newList := s.crashTimes[:0]
	for _, t := range s.crashTimes {
		if t.After(windowStart) {
			newList = append(newList, t)
		}
	}
	s.crashTimes = newList

	// Append the new crash
	s.crashTimes = append(s.crashTimes, now)
	if len(s.crashTimes) > crashLoopThreshold {
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// Metrics & Health Server
// ─────────────────────────────────────────────────────────────────────────────

func (s *Supervisor) startMetricsServer() {
	// Gauge for uptimes
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				uptimeGauge.Set(time.Since(s.startTime).Seconds())
			case <-s.done:
				return
			}
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	slog.Info("Metrics/health listening", slog.String("port", healthPort))
	server := &http.Server{
		Addr:    ":" + healthPort,
		Handler: mux,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("Metrics server error", slog.String("err", err.Error()))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Worker (Child) – Fiber App with Graceful Shutdown
// ─────────────────────────────────────────────────────────────────────────────

func runWorker() {
	// Load .env (child always starts fresh)
	if err := godotenv.Load(envPath); err != nil {
		slog.Warn("Could not load .env", slog.String("err", err.Error()))
	}

	// Listen for signals to gracefully shut down Fiber
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("Child received shutdown signal")
		cancel()
	}()

	// Start the Fiber server
	if err := runFiberServer(ctx); err != nil {
		slog.Error("Fiber server error", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func runFiberServer(ctx context.Context) error {
	app := fiber.New()

	// Example routes
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Fiber app is running!")
	})
	app.Get("/readyz", func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	// Start in background so we can listen for ctx.Done()
	errCh := make(chan error, 1)
	go func() {
		port := os.Getenv("PORT")
		if port == "" {
			port = "3000"
		}
		slog.Info("Child starting Fiber server", slog.String("port", port))
		errCh <- app.Listen(":" + port)
	}()
	<-ctx.Done()
	slog.Info("Draining Fiber server connections and shutting down")
	_ = app.Shutdown()
	return <-errCh
}
