package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
)

const (
	ProtocolID = "/p2p-chat-file/1.0.0"
	Rendezvous = "p2p-chat-file-rendezvous"
)

// discoveryNotifee is used by mDNS to notify us about peers.
type discoveryNotifee struct {
	h   host.Host
	ctx context.Context
}

// fileRequest carries an incoming file transfer request.
type fileRequest struct {
	stream   network.Stream
	fileName string
}

var (
	// inputChan receives all lines typed by the user.
	inputChan = make(chan string)
	// fileRequestChan receives file transfer requests from incoming streams.
	fileRequestChan = make(chan fileRequest)
	// currentDir is the save directory (default is the current directory).
	currentDir = "./"
	mu         sync.Mutex
)

func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	n.h.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstore.PermanentAddrTTL)
	err := n.h.Connect(n.ctx, pi)
	if err != nil {
		fmt.Printf("[⚠️] Could not connect to peer %s: %v\n", pi.ID, err)
	} else {
		fmt.Printf("[✅] Connected to peer: %s\n", pi.ID)
	}
}

func main() {
	ctx := context.Background()
	h, err := libp2p.New()
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()

	fmt.Printf("[🔗] Peer ID: %s\n", h.ID())
	for _, addr := range h.Addrs() {
		fmt.Printf("[🌐] Listening on: %s/p2p/%s\n", addr, h.ID())
	}

	// Start mDNS discovery.
	mdnsService := mdns.NewMdnsService(h, Rendezvous, &discoveryNotifee{h: h, ctx: ctx})
	if err := mdnsService.Start(); err != nil {
		log.Fatal(err)
	}

	// Set stream handler.
	h.SetStreamHandler(ProtocolID, handleStream)

	// Start a goroutine to read user input and send to inputChan.
	go readInput()

	// Main loop: process file requests and user commands from the same input channel.
	for {
		select {
		// A file request from a remote peer.
		case req := <-fileRequestChan:
			processFileRequest(req)
		// A regular user command.
		case line := <-inputChan:
			processCommand(line, h)
		}
	}
}

// readInput continuously reads from os.Stdin and sends each line to inputChan.
func readInput() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		inputChan <- line
	}
	if err := scanner.Err(); err != nil {
		fmt.Println("Error reading from stdin:", err)
	}
}

// handleStream processes incoming streams.
// For chat messages, it reads the message and closes the stream.
// For file transfers, it pushes a fileRequest into fileRequestChan without closing the stream.
func handleStream(s network.Stream) {
	reader := bufio.NewReader(s)
	data, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("[⚠️] Error reading stream:", err)
		s.Close()
		return
	}
	data = strings.TrimSpace(data)

	if strings.HasPrefix(data, "chat:") {
		fmt.Printf("[💬] %s: %s\n", s.Conn().RemotePeer(), strings.TrimPrefix(data, "chat:"))
		s.Close()
	} else if strings.HasPrefix(data, "file:") {
		parts := strings.SplitN(data, ":", 2)
		if len(parts) < 2 {
			fmt.Println("[⚠️] Invalid file request received.")
			s.Close()
			return
		}
		fileName := strings.TrimSpace(parts[1])
		// Do not close the stream: main loop will handle it after the file transfer.
		fileRequestChan <- fileRequest{stream: s, fileName: fileName}
	}
}

// processFileRequest prompts the user to accept or reject the incoming file.
// It uses the shared inputChan for reading the user's response.
func processFileRequest(req fileRequest) {
	fmt.Printf("\nIncoming file request from %s: %s\n", req.stream.Conn().RemotePeer(), req.fileName)
	fmt.Print("Accept file transfer? (y/n): ")
	answer := <-inputChan
	answer = strings.TrimSpace(answer)
	if strings.ToLower(answer) != "y" {
		fmt.Println("❌ File transfer rejected.")
		req.stream.Close()
		return
	}
	fmt.Print("Enter save directory (or press Enter for default): ")
	saveDir := <-inputChan
	saveDir = strings.TrimSpace(saveDir)
	if saveDir == "" {
		mu.Lock()
		saveDir = currentDir
		mu.Unlock()
	}
	// Construct the full path.
	savePath := filepath.Join(saveDir, req.fileName)

	file, err := os.Create(savePath)
	if err != nil {
		fmt.Printf("[⚠️] Error creating file: %v\n", err)
		req.stream.Close()
		return
	}
	fmt.Printf("[📂] Receiving file: %s\n", savePath)
	_, err = io.Copy(file, req.stream)
	if err != nil {
		fmt.Printf("[⚠️] Error receiving file: %v\n", err)
		file.Close()
		req.stream.Close()
		return
	}
	file.Close()
	req.stream.Close()
	fmt.Printf("[✅] File received: %s\n", savePath)
}

// processCommand handles commands from the user.
func processCommand(line string, h host.Host) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	switch {
	case strings.HasPrefix(line, "/chat "):
		message := strings.TrimPrefix(line, "/chat ")
		sendMessage(h, message)
	case strings.HasPrefix(line, "/sendfile "):
		filename := strings.TrimPrefix(line, "/sendfile ")
		sendFile(h, filename)
	case strings.HasPrefix(line, "/setdir "):
		dir := strings.TrimPrefix(line, "/setdir ")
		mu.Lock()
		currentDir = strings.TrimSpace(dir)
		mu.Unlock()
		fmt.Printf("[📂] Save directory set to: %s\n", currentDir)
	case line == "/peers":
		listPeers(h)
	case line == "/exit":
		fmt.Println("Exiting...")
		os.Exit(0)
	default:
		fmt.Println("Invalid command.")
	}
}

// sendMessage sends a chat message to all connected peers.
func sendMessage(h host.Host, message string) {
	for _, p := range h.Peerstore().Peers() {
		if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
			s.Write([]byte("chat:" + message + "\n"))
			s.Close()
		}
	}
}

// sendFile sends a file to all connected peers.
func sendFile(h host.Host, filename string) {
	file, err := os.Open(filename)
	if err != nil {
		fmt.Println("[⚠️] Error opening file.")
		return
	}
	defer file.Close()

	for _, p := range h.Peerstore().Peers() {
		if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
			fmt.Printf("[📂] Sending file: %s\n", filename)
			filename = filepath.Base(filename)
			s.Write([]byte("file:" + filename + "\n"))
			io.Copy(s, file)
			s.Close()
			// Reset file pointer for subsequent peers.
			file.Seek(0, 0)
		}
	}
}

// listPeers prints all connected peers.
func listPeers(h host.Host) {
	fmt.Println("[🌐] Connected peers:")
	for _, p := range h.Peerstore().Peers() {
		if len(h.Peerstore().Addrs(p)) > 0 {
			fmt.Println("-", p)
		}
	}
}
