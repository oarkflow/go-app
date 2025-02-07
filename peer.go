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

type discoveryNotifee struct {
	h   host.Host
	ctx context.Context
}

type fileRequest struct {
	stream   network.Stream
	fileName string
}

var (
	inputChan       = make(chan string)
	fileRequestChan = make(chan fileRequest)
	currentDir      = "./files"
	mu              sync.Mutex
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
	mdnsService := mdns.NewMdnsService(h, Rendezvous, &discoveryNotifee{h: h, ctx: ctx})
	if err := mdnsService.Start(); err != nil {
		log.Fatal(err)
	}
	h.SetStreamHandler(ProtocolID, handleStream)
	go readInput()
	for {
		select {
		case req := <-fileRequestChan:
			processFileRequest(req)
		case line := <-inputChan:
			processCommand(line, h)
		}
	}
}

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
		fileRequestChan <- fileRequest{stream: s, fileName: fileName}
	}
}

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

func processCommand(line string, h host.Host) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	switch {
	case strings.HasPrefix(line, "/chat "):
		message := strings.TrimSpace(strings.TrimPrefix(line, "/chat "))
		sendMessage(h, message)
	case strings.HasPrefix(line, "/sendfile "):
		filename := strings.TrimSpace(strings.TrimPrefix(line, "/sendfile "))
		sendFile(h, filename)
	case strings.HasPrefix(line, "/setdir "):
		mu.Lock()
		currentDir = strings.TrimSpace(strings.TrimPrefix(line, "/setdir "))
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

func sendMessage(h host.Host, message string) {
	for _, p := range h.Peerstore().Peers() {
		if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
			s.Write([]byte("chat:" + message + "\n"))
			s.Close()
		}
	}
}

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

			file.Seek(0, 0)
		}
	}
}

func listPeers(h host.Host) {
	fmt.Println("[🌐] Connected peers:")
	for _, p := range h.Peerstore().Peers() {
		if len(h.Peerstore().Addrs(p)) > 0 {
			fmt.Println("-", p)
		}
	}
}
