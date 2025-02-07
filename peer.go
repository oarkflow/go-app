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

var (
	currentDir = "./"
)

type discoveryNotifee struct {
	h   host.Host
	ctx context.Context
}

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
	go handleUserInput(h)

	select {}
}

func handleStream(s network.Stream) {
	defer s.Close()

	reader := bufio.NewReader(s)
	data, _ := reader.ReadString('\n')

	if strings.HasPrefix(data, "chat:") {
		fmt.Printf("[💬] %s: %s", s.Conn().RemotePeer(), strings.TrimPrefix(data, "chat:"))
	} else if strings.HasPrefix(data, "file:") {
		parts := strings.SplitN(data, ":", 2)
		fileName := strings.TrimSpace(parts[1])
		fmt.Printf("\nIncoming file request: %s\n", fileName)

		savePath := filepath.Join(currentDir, strings.TrimSpace(fileName))
		fmt.Printf("[📂] Receiving file: %s\n", fileName)
		file, err := os.Create(savePath)
		if err != nil {
			fmt.Printf("Error creating file: %v\n", err)
			return
		}
		defer file.Close()

		_, err = io.Copy(file, s)
		if err != nil {
			fmt.Printf("Error receiving file: %v\n", err)
			return
		}

		fmt.Printf("[✅] File received: %s\n> ", savePath)
	}
}

func handleUserInput(h host.Host) {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("Command (/chat message, /send-file filename, /set-dir path, /peers, /exit): ")
		scanner.Scan()
		input := scanner.Text()

		switch {
		case strings.HasPrefix(input, "/chat "):
			message := strings.TrimPrefix(input, "/chat ")
			sendMessage(h, message)
		case strings.HasPrefix(input, "/set-dir "):
			currentDir = strings.TrimSpace(strings.TrimPrefix(input, "/set-dir "))
			os.MkdirAll(currentDir, os.ModePerm)
			fmt.Printf("[✅] Current directory set to: %s\n> ", currentDir)
		case strings.HasPrefix(input, "/send-file "):
			filename := strings.TrimPrefix(input, "/send-file ")
			sendFile(h, filename)
			fmt.Printf("[✅] File sent: %s\n> ", filename)
		case input == "/peers":
			listPeers(h)
		case input == "/exit":
			fmt.Println("Exiting...")
			os.Exit(0)
		default:
			fmt.Println("Invalid command.")
		}
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
	fileName := filepath.Base(filename)
	file, err := os.Open(filename)
	if err != nil {
		fmt.Println("[⚠️] Error opening file.")
		return
	}
	defer file.Close()

	for _, p := range h.Peerstore().Peers() {
		if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
			s.Write([]byte("file:" + fileName + "\n"))
			io.Copy(s, file)
			s.Close()
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
