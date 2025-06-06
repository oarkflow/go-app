package main

import (
	"context" // for JSON env files
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"log/slog"

	"github.com/fsnotify/fsnotify"
	"github.com/natefinch/lumberjack"
	"github.com/oarkflow/dag/supervisor/app"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ─────────────────────────────────────────────────────────────────────────────
// Configuration Constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	// Comma‐separated list of files to watch/load as env sources:
	envPath = ".env, databases.json"

	// Graceful‐shutdown time for killing child:
	graceTimeout = 5 * time.Second

	// Debounce delay for FS notifications (in milliseconds):
	debounceDelay = 500 * time.Millisecond

	// Path to PID file for supervisor:
	pidFile = "/tmp/supervisor.pid"

	// Port where metrics & healthz listen:
	healthPort = "9999"

	// Where to write supervisor logs:
	logFilePath = "./log/myapp/supervisor.log"

	// Where to write child logs:
	childLogFilePath = "./log/myapp/child.log"

	// Initial restart backoff, doubles up to maxBackoff:
	restartBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second

	// Crash‐loop protection: if > crashLoopThreshold crashes within crashLoopWindow, exit:
	crashLoopThreshold = 5
	crashLoopWindow    = 1 * time.Minute
)

var (
	// Prometheus metrics:
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
// Supervisor Struct (manages child process, FS watching, metrics, etc.)
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

func (s *Supervisor) initWatcher(envFiles []string) {
	var err error
	s.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		slog.Error("Failed to create fsnotify watcher", slog.String("err", err.Error()))
		os.Exit(1)
	}

	// Watch the supervisor binary itself:
	binPath, _ := os.Executable()
	toWatch := []string{binPath}

	// Add each env file path to watch:
	for _, f := range envFiles {
		if f == "" {
			continue
		}
		// Make sure to watch the absolute path (so symlink moves still trigger events):
		if abs, err := filepath.Abs(f); err == nil {
			toWatch = append(toWatch, abs)
		} else {
			toWatch = append(toWatch, f)
		}
	}

	for _, file := range toWatch {
		if err := s.watcher.Add(file); err != nil {
			slog.Error("Failed to add watcher", slog.String("file", file), slog.String("err", err.Error()))
			os.Exit(1)
		}
		slog.Info("Watching for changes", slog.String("path", file))
	}
}

func (s *Supervisor) watchFiles() {
	// Create a timer that we can reset for debouncing:
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
			// Only respond to Write/Create/Rename; skip Chmod, Remove, etc.
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
		// already queued—ignore
	}
}

func (s *Supervisor) spawnAndMonitor() {
	for {
		// Exit if supervisor is shutting down:
		select {
		case <-s.done:
			return
		default:
		}

		// Reset backoff before launching child:
		s.backoff = restartBackoff

		// Start child process:
		if err := s.startChild(); err != nil {
			slog.Error("Failed to start child process", slog.String("err", err.Error()))
			os.Exit(1)
		}

		// Wait for child exit or for a restart event:
		exitCh := make(chan error, 1)
		go func() {
			exitCh <- s.currentCmd.Wait()
		}()

		select {
		case reason := <-s.restartEvent:
			// SIGHUP or file‐change triggered:
			slog.Info("Supervisor restarting child", slog.String("reason", reason))
			restartCounter.WithLabelValues(reason).Inc()
			s.killChild()
			continue

		case err := <-exitCh:
			// Child exited (crash or normal)
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
			// Backoff before restarting:
			if s.backoffAndSleep() {
				continue
			}
			return

		case <-s.done:
			// Supervisor shutdown requested—kill child and exit:
			s.killChild()
			return
		}
	}
}

func (s *Supervisor) startChild() error {
	s.cmdLock.Lock()
	defer s.cmdLock.Unlock()

	binPath, _ := os.Executable()
	cmd := exec.Command(binPath, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "RUN_AS_CHILD=1")

	// Ensure child logs directory:
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

	// Put child in its own process group for isolation:
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
	_ = proc.Signal(syscall.SIGTERM) // graceful

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

// recordCrashAndCheckLoop returns false if crashes in window exceed threshold:
func (s *Supervisor) recordCrashAndCheckLoop() bool {
	s.crashTimesLock.Lock()
	defer s.crashTimesLock.Unlock()

	now := time.Now()
	windowStart := now.Add(-crashLoopWindow)

	// Purge old timestamps:
	filtered := s.crashTimes[:0]
	for _, t := range s.crashTimes {
		if t.After(windowStart) {
			filtered = append(filtered, t)
		}
	}
	s.crashTimes = filtered

	// Append newest crash:
	s.crashTimes = append(s.crashTimes, now)
	if len(s.crashTimes) > crashLoopThreshold {
		return false
	}
	return true
}

func (s *Supervisor) startMetricsServer() {
	// Update uptime gauge every 5 seconds:
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
		_, _ = w.Write([]byte("ok"))
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
// Child (Worker) Logic: loads env files, runs callback under context
// ─────────────────────────────────────────────────────────────────────────────

func runWorker(callback func(ctx context.Context) error) {
	// Create a cancellable context and listen for SIGINT/SIGTERM:
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("Child received shutdown signal")
		cancel()
	}()

	// Now invoke the callback (e.g. runApp) under this context:
	if err := callback(ctx); err != nil {
		slog.Error("Worker callback returned error", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Example Fiber Server (this is your callback)
// ─────────────────────────────────────────────────────────────────────────────

// ─────────────────────────────────────────────────────────────────────────────
// Logging Setup (supervisor writes to both stdout and a rotating file)
// ─────────────────────────────────────────────────────────────────────────────

func setupSupervisorLogging() {
	// Ensure log directory exists:
	if err := os.MkdirAll(filepath.Dir(logFilePath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Could not create log dir: %v\n", err)
		os.Exit(1)
	}

	// Lumberjack rotating logger for supervisor:
	fileLogger := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    10, // MB
		MaxBackups: 5,  // number of old log files to keep
		MaxAge:     28, // days
		Compress:   true,
	}

	mw := io.MultiWriter(os.Stdout, fileLogger)                                // write to both stdout and file
	handler := slog.NewTextHandler(mw, &slog.HandlerOptions{AddSource: false}) // no file:line on each entry
	slog.SetDefault(slog.New(handler))
}

func checkOrCreatePIDFile() {
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
// Run Function: entrypoint that wires Supervisor vs. Worker based on env
// ─────────────────────────────────────────────────────────────────────────────

func Run(callback func(ctx context.Context) error) {
	// If RUN_AS_CHILD=1, act as the child (invoke callback):
	if os.Getenv("RUN_AS_CHILD") == "1" {
		setupSupervisorLogging() // still use the same logger format for child
		slog.Info("Child starting up")
		runWorker(callback)
		return
	}

	// Supervisor mode:
	setupSupervisorLogging()
	checkOrCreatePIDFile()
	defer removePIDFile()

	slog.Info("Supervisor starting...")

	// Prepare Supervisor struct:
	s := &Supervisor{
		restartEvent: make(chan string, 1),
		done:         make(chan struct{}),
		backoff:      restartBackoff,
		startTime:    time.Now(),
	}

	// Parse envPath into a list of files for watcher:
	var envFiles []string
	for _, raw := range strings.Split(envPath, ",") {
		if f := strings.TrimSpace(raw); f != "" {
			envFiles = append(envFiles, f)
		}
	}

	s.initWatcher(envFiles)
	defer s.watcher.Close()

	// Start watcher, child monitor, and metrics server:
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
			default: // SIGINT or SIGTERM
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

func main() {
	Run(app.Run)
}
