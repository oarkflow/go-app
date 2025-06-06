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

const (
	envPath            = ".env, databases.json"
	graceTimeout       = 5 * time.Second
	debounceDelay      = 500 * time.Millisecond
	pidFile            = "/tmp/supervisor.pid"
	healthPort         = "9999"
	logFilePath        = "./log/myapp/supervisor.log"
	childLogFilePath   = "./log/myapp/child.log"
	restartBackoff     = 1 * time.Second
	maxBackoff         = 30 * time.Second
	crashLoopThreshold = 5
	crashLoopWindow    = 1 * time.Minute
)

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
	binPath, _ := os.Executable()
	toWatch := []string{binPath}
	for _, f := range envFiles {
		if f == "" {
			continue
		}
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
	timer := time.NewTimer(0)
	<-timer.C
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
	}
}

func (s *Supervisor) spawnAndMonitor() {
	for {
		select {
		case <-s.done:
			return
		default:
		}
		s.backoff = restartBackoff
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
	binPath, _ := os.Executable()
	cmd := exec.Command(binPath, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "RUN_AS_CHILD=1")
	if err := os.MkdirAll(filepath.Dir(childLogFilePath), 0o755); err != nil {
		slog.Error("Failed to create child log dir", slog.String("err", err.Error()))
		return err
	}
	childFileLogger := &lumberjack.Logger{
		Filename:   childLogFilePath,
		MaxSize:    10,
		MaxBackups: 5,
		MaxAge:     28,
		Compress:   true,
	}
	cmd.Stdout = io.MultiWriter(os.Stdout, childFileLogger)
	cmd.Stderr = io.MultiWriter(os.Stderr, childFileLogger)
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
	_ = proc.Signal(syscall.SIGTERM)
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

func (s *Supervisor) recordCrashAndCheckLoop() bool {
	s.crashTimesLock.Lock()
	defer s.crashTimesLock.Unlock()
	now := time.Now()
	windowStart := now.Add(-crashLoopWindow)
	filtered := s.crashTimes[:0]
	for _, t := range s.crashTimes {
		if t.After(windowStart) {
			filtered = append(filtered, t)
		}
	}
	s.crashTimes = filtered
	s.crashTimes = append(s.crashTimes, now)
	if len(s.crashTimes) > crashLoopThreshold {
		return false
	}
	return true
}

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

func runWorker(callback func(ctx context.Context) error) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("Child received shutdown signal")
		cancel()
	}()
	if err := callback(ctx); err != nil {
		slog.Error("Worker callback returned error", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func setupSupervisorLogging() {
	if err := os.MkdirAll(filepath.Dir(logFilePath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Could not create log dir: %v\n", err)
		os.Exit(1)
	}
	fileLogger := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    10,
		MaxBackups: 5,
		MaxAge:     28,
		Compress:   true,
	}
	mw := io.MultiWriter(os.Stdout, fileLogger)
	handler := slog.NewTextHandler(mw, &slog.HandlerOptions{AddSource: false})
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

func Run(callback func(ctx context.Context) error) {
	if os.Getenv("RUN_AS_CHILD") == "1" {
		setupSupervisorLogging()
		slog.Info("Child starting up")
		runWorker(callback)
		return
	}
	setupSupervisorLogging()
	checkOrCreatePIDFile()
	defer removePIDFile()
	slog.Info("Supervisor starting...")
	s := &Supervisor{
		restartEvent: make(chan string, 1),
		done:         make(chan struct{}),
		backoff:      restartBackoff,
		startTime:    time.Now(),
	}
	var envFiles []string
	for _, raw := range strings.Split(envPath, ",") {
		if f := strings.TrimSpace(raw); f != "" {
			envFiles = append(envFiles, f)
		}
	}
	s.initWatcher(envFiles)
	defer s.watcher.Close()
	go s.watchFiles()
	go s.spawnAndMonitor()
	go s.startMetricsServer()
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

func main() {
	Run(app.Run)
}
