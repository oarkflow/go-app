// main.go

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"log/slog"

	"github.com/fsnotify/fsnotify"
	"github.com/natefinch/lumberjack"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ─────────────────────────────────────────────────────────────────────────────
// Configuration
// ─────────────────────────────────────────────────────────────────────────────

const (
	// Debounce delay for file events
	debounceDelay = 500 * time.Millisecond
	// Graceful shutdown timeout for child
	graceTimeout = 5 * time.Second

	// Crash-loop threshold: if child crashes > this within crashLoopWindow, exit
	crashLoopThreshold = 5
	crashLoopWindow    = 1 * time.Minute

	// Prometheus & health endpoint port
	healthPort = "9999"

	// Log files
	logFilePath      = "./log/myapp/supervisor.log"
	childLogFilePath = "./log/myapp/child.log"
)

// CLI flags
var (
	cmdFlag = flag.String("cmd", "", "Command to run as child (e.g., 'go run app.go')")
	envFlag = flag.String("env", ".env", "Path to .env file to load and watch")
)

// Prometheus metrics
var (
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
	prometheus.MustRegister(restartCounter, crashCounter, uptimeGauge)
}

// Supervisor manages the child process and watchers
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

	envPath      string            // path to .env from --env
	childCommand string            // full command line from --cmd
	lastEnvHash  string            // SHA256 of .env contents
	lastOSEnv    map[string]string // snapshot of OS environment
}

func main() {
	flag.Parse()

	if *cmdFlag == "" {
		fmt.Fprintln(os.Stderr, "Error: --cmd is required")
		os.Exit(1)
	}

	// Wrap the entire cmdFlag under /bin/sh -c so its children share the same PGID
	childCmd := *cmdFlag

	setupLogging()
	checkOrCreatePIDFile()
	defer removePIDFile()

	slog.Info("Supervisor starting...")

	s := &Supervisor{
		restartEvent: make(chan string, 1),
		done:         make(chan struct{}),
		backoff:      1 * time.Second,
		startTime:    time.Now(),
		envPath:      *envFlag,
		childCommand: childCmd,
		lastOSEnv:    make(map[string]string),
	}

	// Compute initial .env hash and capture OS env
	s.computeEnvHash()
	s.captureOSEnv()

	// Initialize fsnotify watcher
	s.initWatcher()
	defer s.watcher.Close()

	// Start background tasks
	go s.watchFiles()         // watch .env file, binary, AWS files
	go s.watchEnvAndOSEnv()   // poll .env hash + OS env
	go s.spawnAndMonitor()    // spawn & monitor child
	go s.startMetricsServer() // /metrics + /healthz

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

// ─────────────────────────────────────────────────────────────────────────────
// Logging (stdout + rotating file)
// ─────────────────────────────────────────────────────────────────────────────

func setupLogging() {
	// Ensure log directory exists
	if err := os.MkdirAll(filepath.Dir(logFilePath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Could not create log dir: %v\n", err)
		os.Exit(1)
	}
	fileLogger := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    10, // MB
		MaxBackups: 5,
		MaxAge:     28, // days
		Compress:   true,
	}
	mw := io.MultiWriter(os.Stdout, fileLogger)
	handler := slog.NewTextHandler(mw, &slog.HandlerOptions{AddSource: false})
	slog.SetDefault(slog.New(handler))
}

// ─────────────────────────────────────────────────────────────────────────────
// PID File Locking
// ─────────────────────────────────────────────────────────────────────────────

func checkOrCreatePIDFile() {
	const pidFile = "/tmp/supervisor.pid"
	if _, err := os.Stat(pidFile); err == nil {
		slog.Error("PID file already exists; another instance may be running",
			slog.String("file", pidFile))
		os.Exit(1)
	}
	pid := os.Getpid()
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", pid)), 0o644); err != nil {
		slog.Error("Failed to write PID file", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func removePIDFile() {
	_ = os.Remove("/tmp/supervisor.pid")
}

// ─────────────────────────────────────────────────────────────────────────────
// File Watching (.env, supervisor binary, AWS config/credentials)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Supervisor) initWatcher() {
	var err error
	s.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		slog.Error("Failed to create fsnotify watcher", slog.String("err", err.Error()))
		os.Exit(1)
	}

	// Watch each env file directory.
	envFiles := strings.Split(s.envPath, ",")
	for _, file := range envFiles {
		file = strings.TrimSpace(file)
		if abs, err := filepath.Abs(file); err == nil {
			dir := filepath.Dir(abs)
			if err := s.watcher.Add(dir); err != nil {
				slog.Error("Failed to watch env directory",
					slog.String("file", file), slog.String("err", err.Error()))
				os.Exit(1)
			}
			slog.Info("Watching env file", slog.String("path", abs))
		} else {
			slog.Error("Unable to resolve env file", slog.String("file", file), slog.String("err", err.Error()))
			os.Exit(1)
		}
	}

	// Watch the directory containing this supervisor binary
	if bin, err := os.Executable(); err == nil {
		if abs, err := filepath.Abs(bin); err == nil {
			dir := filepath.Dir(abs)
			if err := s.watcher.Add(dir); err != nil {
				slog.Error("Failed to watch binary directory",
					slog.String("dir", dir), slog.String("err", err.Error()))
				os.Exit(1)
			}
			slog.Info("Watching supervisor binary directory",
				slog.String("path", dir))
		}
	}

	// Watch AWS config/credentials (if they exist)
	home := os.Getenv("HOME")
	awsFiles := []string{
		filepath.Join(home, ".aws", "config"),
		filepath.Join(home, ".aws", "credentials"),
	}
	for _, f := range awsFiles {
		if abs, err := filepath.Abs(f); err == nil {
			dir := filepath.Dir(abs)
			if err := s.watcher.Add(dir); err == nil {
				slog.Info("Watching AWS config directory", slog.String("path", dir))
			}
		}
	}
}

func (s *Supervisor) watchFiles() {
	timer := time.NewTimer(0)
	<-timer.C // drain

	var mu sync.Mutex
	debounce := func(reason string) {
		// If we're already shutting down, do nothing
		select {
		case <-s.done:
			return
		default:
		}

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
				slog.Info("File event detected",
					slog.String("file", event.Name),
					slog.String("op", event.Op.String()))
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
	case <-s.done:
		return
	case s.restartEvent <- reason:
	default:
		// Already queued
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// .env and OS Environment Polling
// ─────────────────────────────────────────────────────────────────────────────

// computeEnvHash reads s.envPath, sorts its key=value lines, and computes SHA256
func (s *Supervisor) computeEnvHash() {
	// Support multiple env files separated by comma.
	files := strings.Split(s.envPath, ",")
	// Sort filenames for consistent ordering
	sort.Strings(files)
	var buf bytes.Buffer
	for _, file := range files {
		file = strings.TrimSpace(file)
		data, err := os.ReadFile(file)
		if err != nil {
			// Skip missing or unreadable files.
			continue
		}
		// Append filename and content to differentiate each file.
		buf.WriteString("file:" + file + "\n")
		buf.Write(data)
		buf.WriteString("\n")
	}
	hash := sha256.Sum256(buf.Bytes())
	s.lastEnvHash = hex.EncodeToString(hash[:])
}

// captureOSEnv snapshots os.Environ()
func (s *Supervisor) captureOSEnv() {
	s.lastOSEnv = make(map[string]string)
	for _, kv := range os.Environ() {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			s.lastOSEnv[parts[0]] = parts[1]
		}
	}
}

// detectOSEnvChanges compares current os.Environ() vs s.lastOSEnv, logs diffs, returns true if changed
func (s *Supervisor) detectOSEnvChanges() bool {
	current := make(map[string]string)
	for _, kv := range os.Environ() {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			current[parts[0]] = parts[1]
		}
	}
	changed := false
	for k, v := range current {
		if ov, ok := s.lastOSEnv[k]; !ok {
			slog.Info("OS env added", slog.String("key", k), slog.String("val", v))
			changed = true
		} else if ov != v {
			slog.Info("OS env changed", slog.String("key", k), slog.String("from", ov), slog.String("to", v))
			changed = true
		}
	}
	for k := range s.lastOSEnv {
		if _, ok := current[k]; !ok {
			slog.Info("OS env removed", slog.String("key", k))
			changed = true
		}
	}
	if changed {
		s.lastOSEnv = current
	}
	return changed
}

// watchEnvAndOSEnv polls .env file hash + OS env every 5s
func (s *Supervisor) watchEnvAndOSEnv() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Check OS env
			if s.detectOSEnvChanges() {
				s.queueRestart("os_env")
			}
		case <-s.done:
			return
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Child Process Lifecycle
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

	if s.childCommand == "" {
		return errors.New("no command specified")
	}
	// Wrap entire command in /bin/sh -c "..."
	cmd := exec.Command("/bin/sh", "-c", s.childCommand)
	// Do not modify OS env by loading env file; simply inherit existing environment.
	cmd.Env = os.Environ()

	// Put this process (shell) into its own PGID
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Child logs to rotating file + stdout
	if err := os.MkdirAll(filepath.Dir(childLogFilePath), 0o755); err != nil {
		slog.Error("Failed to create child log dir", slog.String("err", err.Error()))
		return err
	}
	childRotator := &lumberjack.Logger{
		Filename:   childLogFilePath,
		MaxSize:    10, // MB
		MaxBackups: 5,
		MaxAge:     28, // days
		Compress:   true,
	}
	cmd.Stdout = io.MultiWriter(os.Stdout, childRotator)
	cmd.Stderr = io.MultiWriter(os.Stderr, childRotator)

	if err := cmd.Start(); err != nil {
		return err
	}
	s.currentCmd = cmd
	slog.Info("Spawned child process",
		slog.Int("pid", cmd.Process.Pid),
		slog.String("cmd", s.childCommand))
	return nil
}

func (s *Supervisor) killChild() {
	s.cmdLock.Lock()
	defer s.cmdLock.Unlock()

	if s.currentCmd == nil || s.currentCmd.Process == nil {
		return
	}
	proc := s.currentCmd.Process
	pgid, err := syscall.Getpgid(proc.Pid)
	if err != nil {
		// Fallback: kill only the top process
		_ = proc.Signal(syscall.SIGTERM)
	} else {
		// Send SIGTERM to entire PGID (negative)
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	}

	done := make(chan struct{})
	go func() {
		s.currentCmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("Child terminated gracefully")
	case <-time.After(graceTimeout):
		slog.Warn("Child did not exit in time; sending SIGKILL to group")
		if pgid >= 0 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = proc.Kill()
		}
		<-done
	}
	s.currentCmd = nil
}

func (s *Supervisor) resetBackoff() {
	s.backoff = 1 * time.Second
}

func (s *Supervisor) backoffAndSleep() bool {
	d := s.backoff
	if s.backoff < 30*time.Second {
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

func (s *Supervisor) recordCrashAndCheckLoop() bool {
	s.crashTimesLock.Lock()
	defer s.crashTimesLock.Unlock()

	now := time.Now()
	windowStart := now.Add(-crashLoopWindow)

	// Purge timestamps older than windowStart
	newList := s.crashTimes[:0]
	for _, t := range s.crashTimes {
		if t.After(windowStart) {
			newList = append(newList, t)
		}
	}
	s.crashTimes = newList

	// Append current crash
	s.crashTimes = append(s.crashTimes, now)
	return len(s.crashTimes) <= crashLoopThreshold
}

// ─────────────────────────────────────────────────────────────────────────────
// Metrics & Health Server
// ─────────────────────────────────────────────────────────────────────────────

func (s *Supervisor) startMetricsServer() {
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
	err := server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		slog.Error("Metrics server error", slog.String("err", err.Error()))
	}
}
