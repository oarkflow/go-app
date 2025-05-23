package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	WatchDir         = "/etc"
	PollInterval     = 5 * time.Second
	AuthLogPath      = "/var/log/auth.log"
	HTTPAddr         = ":8080"
	RateLimitWindow  = 1 * time.Minute
	MaxRequestsPerIP = 60
	BadIPsFile       = "bad_ips.txt"
)

// rate‐limiter state
var (
	reqCounts   = make(map[string]int)
	reqMutex    sync.Mutex
	badIPLookup = make(map[string]struct{})
	sqlSigs     = []string{"' or '1'='1", "union select", "-- ", ";--", "xp_"}
	xssSigs     = []string{"<script>", "javascript:", "onerror=", "onload="}
)

func main() {
	loadBadIPs(BadIPsFile)

	// start modules
	go networkSniffer() // linux: real; other: stub
	go monitorFiles(WatchDir)
	go monitorAuthLog(AuthLogPath)
	go startHTTPServer()

	// block forever
	select {}
}

// load bad IP feed
func loadBadIPs(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Println("WARN: cannot load bad IPs:", err)
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		ip := strings.TrimSpace(scanner.Text())
		if ip != "" {
			badIPLookup[ip] = struct{}{}
		}
	}
}

// ---------------------------------------------
// HTTP server & middleware
// ---------------------------------------------
func startHTTPServer() {
	mux := http.NewServeMux()
	handler := rateLimit(detectAppAttacks(http.HandlerFunc(indexHandler)))
	mux.Handle("/", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "OK")
	})
	fmt.Println("HTTP listening on", HTTPAddr)
	http.ListenAndServe(HTTPAddr, mux)
}

func indexHandler(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintln(w, "Welcome to CyberGuard!")
}

func rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		reqMutex.Lock()
		reqCounts[ip]++
		count := reqCounts[ip]
		reqMutex.Unlock()

		go func() {
			time.Sleep(RateLimitWindow)
			reqMutex.Lock()
			reqCounts[ip]--
			reqMutex.Unlock()
		}()

		if count > MaxRequestsPerIP {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			fmt.Println("Rate limit:", ip)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func detectAppAttacks(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		payload := strings.ToLower(r.URL.RawQuery + string(body))
		for _, sig := range sqlSigs {
			if strings.Contains(payload, sig) {
				http.Error(w, "SQL Injection Detected", http.StatusForbidden)
				return
			}
		}
		for _, sig := range xssSigs {
			if strings.Contains(payload, sig) {
				http.Error(w, "XSS Detected", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------
// File‐system monitor
// ---------------------------------------------
func monitorFiles(root string) {
	seen := make(map[string]time.Time)
	for {
		changes := 0
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if prev, ok := seen[path]; !ok || info.ModTime().After(prev) {
				seen[path] = info.ModTime()
				changes++
				fmt.Println("File changed:", path)
				if strings.HasSuffix(path, "/passwd") {
					isolateHost()
				}
			}
			return nil
		})
		if changes > 100 {
			fmt.Println("High file‐change rate:", changes)
		}
		time.Sleep(PollInterval)
	}
}

// ---------------------------------------------
// Auth log monitor
// ---------------------------------------------
func monitorAuthLog(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Println("Auth log not found or unsupported on", runtime.GOOS)
		return
	}
	defer f.Close()
	f.Seek(0, io.SeekEnd)
	rdr := bufio.NewReader(f)
	for {
		line, err := rdr.ReadString('\n')
		if err == io.EOF {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err != nil {
			return
		}
		if strings.Contains(line, "Failed password") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				blockIP(parts[len(parts)-4])
			}
		}
	}
}

// ---------------------------------------------
// Cross‐platform firewall
// ---------------------------------------------
func blockIP(ip string) {
	fmt.Println("Blocking IP:", ip)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("iptables", "-A", "INPUT", "-s", ip, "-j", "DROP")
	case "darwin":
		cmd = exec.Command("pfctl", "-t", "blocked", "-T", "add", ip)
	case "windows":
		cmd = exec.Command("netsh", "advfirewall", "firewall",
			"add", "rule", "name=CyberGuardBlock", "dir=in", "action=block", "remoteip="+ip)
	default:
		fmt.Println("Unsupported OS for blocking:", runtime.GOOS)
		return
	}
	if err := cmd.Run(); err != nil {
		fmt.Println("Error running block command:", err)
	}
}

func isolateHost() {
	fmt.Println("Isolating host:", runtime.GOOS)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("iptables", "-A", "OUTPUT", "-j", "DROP")
	case "darwin":
		cmd = exec.Command("pfctl", "-t", "blocked_out", "-T", "add", "0.0.0.0/0")
	case "windows":
		cmd = exec.Command("netsh", "advfirewall", "set", "allprofiles", "firewallpolicy", "blockinbound,blockoutbound")
	default:
		fmt.Println("Unsupported OS for isolation:", runtime.GOOS)
		return
	}
	if err := cmd.Run(); err != nil {
		fmt.Println("Error isolating host:", err)
	}
}

// analyzePacket is now shared across Linux, macOS, and Windows.
func analyzePacket(pkt []byte) {
	// Only IPv4
	if len(pkt) < 20 {
		return
	}
	// Ethernet vs. raw‐IP: if Ethernet frame, skip the first 14 bytes
	var ipStart int
	ethType := binary.BigEndian.Uint16(pkt[12:14])
	if ethType == 0x0800 && len(pkt) >= 34 {
		ipStart = 14
	} else {
		ipStart = 0
	}
	// Ensure we have full IPv4 header
	if len(pkt) < ipStart+20 {
		return
	}
	header := pkt[ipStart:]
	ihl := int(header[0]&0x0F) * 4
	if len(header) < ihl {
		return
	}

	proto := header[9]
	srcIP := net.IP(header[12:16]).String()

	// 1) Block known bad IPs
	if _, bad := badIPLookup[srcIP]; bad {
		fmt.Println("Matched threat feed, blocking:", srcIP)
		blockIP(srcIP)
		return
	}

	// 2) TCP SYN flood (SYN w/o ACK)
	if proto == 6 && len(header) >= ihl+14 {
		flags := header[ihl+13]
		const synFlag, ackFlag = 0x02, 0x10
		if flags&synFlag != 0 && flags&ackFlag == 0 {
			fmt.Println("SYN flood detected from", srcIP)
			blockIP(srcIP)
			return
		}
	}

	// 3) (Optional) ICMP/UDP flood hooks could go here...
	//    e.g. if proto==1 { /* ICMP logic */ }
	//         if proto==17 { /* UDP logic */ }
}
