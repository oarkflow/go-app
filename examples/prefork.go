// prefork_full.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

const (
	childEnv        = "IS_CHILD"
	listenAddr      = ":8081"
	appVersion      = "1.0.0"
	shutdownTimeout = 10 * time.Second
	
	idempDir       = "./idemp"
	idempHeaderKey = "Idempotency-Key"
)

type storedResponse struct {
	Status  int
	Headers map[string][]string
	Body    []byte
}

func main() {
	// Prepare idempotency storage
	if err := os.MkdirAll(idempDir, 0o755); err != nil {
		log.Fatalf("mkdir idemp dir: %v", err)
	}
	
	if os.Getenv(childEnv) != "" {
		runWorker()
	} else {
		runMaster()
	}
}

func runMaster() {
	log.SetPrefix(fmt.Sprintf("[master %d] ", os.Getpid()))
	log.Println("starting master")
	
	fd := createAndBind(listenAddr)
	listenerFile := os.NewFile(uintptr(fd), "listener")
	
	// Capture shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	
	numCPU := runtime.NumCPU()
	workers := make([]*exec.Cmd, numCPU)
	for i := 0; i < numCPU; i++ {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), childEnv+"=1")
		cmd.ExtraFiles = []*os.File{listenerFile}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			log.Fatalf("fork #%d failed: %v", i, err)
		}
		log.Printf("→ worker #%d PID %d", i, cmd.Process.Pid)
		workers[i] = cmd
	}
	
	// Wait for signal, then propagate
	<-sigCh
	log.Println("shutdown signal received, terminating workers")
	for _, cmd := range workers {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	
	// Wait for workers or timeout
	done := make(chan struct{})
	go func() {
		for _, cmd := range workers {
			cmd.Wait()
			log.Printf("worker PID %d exited", cmd.Process.Pid)
		}
		close(done)
	}()
	select {
	case <-done:
		log.Println("all workers exited, master exiting")
	case <-time.After(shutdownTimeout):
		log.Println("timeout waiting for workers, killing remaining")
		for _, cmd := range workers {
			if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
				_ = cmd.Process.Kill()
			}
		}
	}
}

func createAndBind(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		log.Fatalf("invalid address %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Fatalf("invalid port %q: %v", portStr, err)
	}
	
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		log.Fatalf("socket: %v", err)
	}
	syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEPORT, 1)
	
	sa := &syscall.SockaddrInet4{Port: port}
	if err := syscall.Bind(fd, sa); err != nil {
		log.Fatalf("bind: %v", err)
	}
	if err := syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		log.Fatalf("listen: %v", err)
	}
	return fd
}

func runWorker() {
	pid := os.Getpid()
	log.SetPrefix(fmt.Sprintf("[worker %d] ", pid))
	log.Printf("starting worker")
	
	f := os.NewFile(uintptr(3), "listener")
	ln, err := net.FileListener(f)
	if err != nil {
		log.Fatalf("FileListener: %v", err)
	}
	
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/version", versionHandler)
	mux.HandleFunc("/echo", echoHandler)
	mux.HandleFunc("/time", timeHandler)
	
	handler := loggingMiddleware(idempotentMiddleware(mux))
	
	srv := &http.Server{
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	
	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh
		log.Println("shutdown signal received")
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	
	log.Printf("serving on %s", listenAddr)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Serve error: %v", err)
	}
	log.Println("worker exiting cleanly")
}

// --- Handlers ---

func healthHandler(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"version": appVersion})
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	resp := map[string]interface{}{
		"method": r.Method,
		"path":   r.URL.Path,
		"query":  r.URL.Query(),
		"header": r.Header,
		"body":   string(body),
	}
	json.NewEncoder(w).Encode(resp)
}

func timeHandler(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"utc_time": time.Now().UTC().Format(time.RFC3339)})
}

// --- Middleware ---

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s in %v", r.RemoteAddr, r.Method, r.URL.Path, time.Since(start))
	})
}

func idempotentMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(idempHeaderKey)
		if key == "" {
			// no key → normal processing
			next.ServeHTTP(w, r)
			return
		}
		if !validKey(key) {
			http.Error(w, "invalid idempotency key", http.StatusBadRequest)
			return
		}
		
		path := filepath.Join(idempDir, key+".json")
		// replay cached
		if exists(path) {
			if err := replayResponse(w, path); err != nil {
				log.Printf("replay error: %v", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
			return
		}
		
		// lock file to prevent duplicate
		lock := path + ".lock"
		lf, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			http.Error(w, "duplicate in-flight request", http.StatusConflict)
			return
		}
		lf.Close()
		defer os.Remove(lock)
		
		// record the response
		rec := &responseRecorder{
			headers: make(http.Header),
			status:  http.StatusOK, // default to 200
		}
		next.ServeHTTP(rec, r)
		
		// persist
		sr := storedResponse{
			Status:  rec.status,
			Headers: rec.headers,
			Body:    rec.body.Bytes(),
		}
		tmp := path + ".tmp"
		if f, err := os.Create(tmp); err == nil {
			json.NewEncoder(f).Encode(&sr)
			f.Close()
			os.Rename(tmp, path)
		} else {
			log.Printf("persist create tmp: %v", err)
		}
		
		// write out
		for k, vs := range sr.Headers {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(sr.Status)
		w.Write(sr.Body)
	})
}

// responseRecorder never has a zero status code
type responseRecorder struct {
	headers http.Header
	body    bytes.Buffer
	status  int
}

func (r *responseRecorder) Header() http.Header         { return r.headers }
func (r *responseRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *responseRecorder) WriteHeader(status int) {
	if status < 100 {
		status = http.StatusOK
	}
	if r.status == 0 || status != http.StatusOK {
		r.status = status
	}
}

func validKey(key string) bool {
	for _, r := range key {
		if !(r == '-' || r == '_' ||
			(r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z')) {
			return false
		}
	}
	return key != ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func replayResponse(w http.ResponseWriter, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var sr storedResponse
	if err := json.NewDecoder(f).Decode(&sr); err != nil {
		return err
	}
	for k, vs := range sr.Headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(sr.Status)
	_, err = w.Write(sr.Body)
	return err
}
