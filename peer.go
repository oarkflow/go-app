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

// Protocol and rendezvous string for this app.
const (
	ProtocolID = "/p2p-chat-file/1.0.0"
	Rendezvous = "p2p-chat-file-rendezvous"
)

// Global state for user and rooms.
var (
	username    string = "Anonymous"
	currentRoom string = "public"                         // active room
	rooms              = map[string]bool{"public": true}  // joined rooms
	adminRooms         = map[string]bool{"public": false} // admin status (public room has no admin)
	localPeerID string                                    // our own peer id (as string)

	// For file transfers:
	inputChan       = make(chan string)
	fileRequestChan = make(chan fileRequest)
	currentDir      = "./files"
	mu              sync.Mutex
)

// discoveryNotifee is used with mDNS.
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

// fileRequest carries an incoming file transfer request.
type fileRequest struct {
	stream   network.Stream
	fileName string
}

func main() {
	ctx := context.Background()
	h, err := libp2p.New()
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()

	localPeerID = h.ID().ShortString()

	fmt.Printf("[🔗] Peer ID: %s\n", localPeerID)
	for _, addr := range h.Addrs() {
		fmt.Printf("[🌐] Listening on: %s/p2p/%s\n", addr, h.ID())
	}

	// Ensure file save directory exists.
	os.MkdirAll(currentDir, os.ModePerm)

	mdnsService := mdns.NewMdnsService(h, Rendezvous, &discoveryNotifee{h: h, ctx: ctx})
	if err := mdnsService.Start(); err != nil {
		log.Fatal(err)
	}

	h.SetStreamHandler(ProtocolID, handleStream)

	// Start a goroutine to continuously read user input.
	go readInput()

	// Main loop: process file requests and user commands.
	for {
		select {
		case req := <-fileRequestChan:
			processFileRequest(req)
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

// processCommand handles user commands.
func processCommand(line string, h host.Host) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	switch {
	// Change username.
	case strings.HasPrefix(line, "/username "):
		newName := strings.TrimSpace(strings.TrimPrefix(line, "/username "))
		username = newName
		fmt.Printf("Username changed to %s\n", username)

	// Switch active room (must be joined).
	case strings.HasPrefix(line, "/room "):
		room := strings.TrimSpace(strings.TrimPrefix(line, "/room "))
		if _, ok := rooms[room]; ok {
			currentRoom = room
			fmt.Printf("Switched to room %s\n", room)
		} else {
			fmt.Printf("You are not a member of room %s. Use /joinroom or /createroom.\n", room)
		}

	// Create a new group room (you become admin).
	case strings.HasPrefix(line, "/createroom "):
		room := strings.TrimSpace(strings.TrimPrefix(line, "/createroom "))
		rooms[room] = true
		adminRooms[room] = true
		currentRoom = room
		fmt.Printf("Room %s created. You are admin.\n", room)
		sendBroadcast(h, fmt.Sprintf("join:%s:%s\n", room, username))

	// Join an existing room.
	case strings.HasPrefix(line, "/joinroom "):
		room := strings.TrimSpace(strings.TrimPrefix(line, "/joinroom "))
		rooms[room] = true
		currentRoom = room
		fmt.Printf("Joined room %s\n", room)
		sendBroadcast(h, fmt.Sprintf("join:%s:%s\n", room, username))

	// Invite a peer to a room (admin only).
	case strings.HasPrefix(line, "/invite "):
		parts := strings.Fields(line)
		if len(parts) < 3 {
			fmt.Println("Usage: /invite <roomName> <peerID>")
		} else {
			room := parts[1]
			target := parts[2]
			if admin, ok := adminRooms[room]; !ok || !admin {
				fmt.Println("You are not admin of room", room)
			} else {
				sendToPeer(h, target, fmt.Sprintf("invite:%s:%s\n", room, username))
				fmt.Printf("Invite sent to %s for room %s\n", target, room)
			}
		}

	// Kick a peer from a room (admin only).
	case strings.HasPrefix(line, "/kick "):
		parts := strings.Fields(line)
		if len(parts) < 3 {
			fmt.Println("Usage: /kick <roomName> <peerID>")
		} else {
			room := parts[1]
			target := parts[2]
			if admin, ok := adminRooms[room]; !ok || !admin {
				fmt.Println("You are not admin of room", room)
			} else {
				sendBroadcast(h, fmt.Sprintf("kick:%s:%s:%s\n", room, target, username))
				fmt.Printf("Kick message sent for %s from room %s\n", target, room)
			}
		}

	// Send a private message.
	case strings.HasPrefix(line, "/msg "):
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			fmt.Println("Usage: /msg <peerID> <message>")
		} else {
			target := parts[1]
			msg := parts[2]
			sendToPeer(h, target, fmt.Sprintf("private:%s:%s:%s\n", target, username, msg))
		}

	// Send a chat message to the current room.
	case strings.HasPrefix(line, "/chat "):
		msg := strings.TrimSpace(strings.TrimPrefix(line, "/chat "))
		if currentRoom == "public" {
			sendBroadcast(h, fmt.Sprintf("chat:public:%s:%s\n", username, msg))
		} else {
			sendBroadcast(h, fmt.Sprintf("chat:group:%s:%s:%s\n", currentRoom, username, msg))
		}

	// Send a file.
	case strings.HasPrefix(line, "/sendfile "):
		filename := strings.TrimSpace(strings.TrimPrefix(line, "/sendfile "))
		sendFile(h, filename)

	// Set file save directory.
	case strings.HasPrefix(line, "/setdir "):
		dir := strings.TrimSpace(strings.TrimPrefix(line, "/setdir "))
		mu.Lock()
		currentDir = dir
		mu.Unlock()
		fmt.Printf("Save directory set to: %s\n", currentDir)

	// List connected peers.
	case line == "/peers":
		listPeers(h)

	// Exit the program.
	case line == "/exit":
		fmt.Println("Exiting...")
		os.Exit(0)

	default:
		fmt.Println("Invalid command.")
	}
}

// sendBroadcast sends msg to all connected peers.
func sendBroadcast(h host.Host, msg string) {
	for _, p := range h.Peerstore().Peers() {
		if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
			s.Write([]byte(msg))
			s.Close()
		}
	}
}

// sendToPeer sends msg only to the peer whose Pretty() matches target.
func sendToPeer(h host.Host, target string, msg string) {
	for _, p := range h.Peerstore().Peers() {
		if p.ShortString() == target {
			if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
				s.Write([]byte(msg))
				s.Close()
			}
		}
	}
}

// handleStream processes incoming streams.
func handleStream(s network.Stream) {
	reader := bufio.NewReader(s)
	data, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("[⚠️] Error reading stream:", err)
		s.Close()
		return
	}
	data = strings.TrimSpace(data)

	switch {
	// Chat messages.
	case strings.HasPrefix(data, "chat:"):
		// For public: "chat:public:<username>:<message>"
		// For group: "chat:group:<roomName>:<username>:<message>"
		parts := strings.SplitN(data, ":", 5)
		if len(parts) < 4 {
			fmt.Println("[⚠️] Invalid chat message format.")
			s.Close()
			return
		}
		if parts[1] == "public" {
			fmt.Printf("[Public] %s: %s\n", parts[2], parts[3])
		} else if parts[1] == "group" {
			if len(parts) < 5 {
				fmt.Println("[⚠️] Invalid group chat format.")
				s.Close()
				return
			}
			roomName := parts[2]
			// Only display if you are in that room.
			if _, ok := rooms[roomName]; ok {
				fmt.Printf("[Group:%s] %s: %s\n", roomName, parts[3], parts[4])
			}
		}
		s.Close()

	// Private messages.
	case strings.HasPrefix(data, "private:"):
		// Format: "private:<targetPeerID>:<username>:<message>"
		parts := strings.SplitN(data, ":", 4)
		if len(parts) < 4 {
			fmt.Println("[⚠️] Invalid private message format.")
			s.Close()
			return
		}
		// Since private messages are sent directly, simply display.
		fmt.Printf("[Private] %s: %s\n", parts[2], parts[3])
		s.Close()

	// Invite messages.
	case strings.HasPrefix(data, "invite:"):
		// Format: "invite:<roomName>:<username>"
		parts := strings.SplitN(data, ":", 3)
		if len(parts) < 3 {
			fmt.Println("[⚠️] Invalid invite message format.")
			s.Close()
			return
		}
		roomName := parts[1]
		inviter := parts[2]
		fmt.Printf("[Invite] %s invited you to join room '%s'. To join, type: /joinroom %s\n", inviter, roomName, roomName)
		s.Close()

	// Join notifications.
	case strings.HasPrefix(data, "join:"):
		// Format: "join:<roomName>:<username>"
		parts := strings.SplitN(data, ":", 3)
		if len(parts) < 3 {
			fmt.Println("[⚠️] Invalid join message format.")
			s.Close()
			return
		}
		roomName := parts[1]
		joiner := parts[2]
		fmt.Printf("[Room %s] %s has joined.\n", roomName, joiner)
		s.Close()

	// Kick messages.
	case strings.HasPrefix(data, "kick:"):
		// Format: "kick:<roomName>:<targetPeerID>:<byUsername>"
		parts := strings.SplitN(data, ":", 4)
		if len(parts) < 4 {
			fmt.Println("[⚠️] Invalid kick message format.")
			s.Close()
			return
		}
		roomName := parts[1]
		target := parts[2]
		by := parts[3]
		fmt.Printf("[Room %s] %s was kicked out by %s.\n", roomName, target, by)
		// If you are the target, remove the room.
		if target == localPeerID {
			delete(rooms, roomName)
			if currentRoom == roomName {
				currentRoom = "public"
			}
			fmt.Printf("You have been removed from room %s. Switched to public room.\n", roomName)
		}
		s.Close()

	// File transfer request.
	case strings.HasPrefix(data, "file:"):
		parts := strings.SplitN(data, ":", 2)
		if len(parts) < 2 {
			fmt.Println("[⚠️] Invalid file request received.")
			s.Close()
			return
		}
		fileName := strings.TrimSpace(parts[1])
		// Pass the request to the file transfer channel.
		fileRequestChan <- fileRequest{stream: s, fileName: fileName}

	default:
		fmt.Printf("[⚠️] Unknown message type: %s\n", data)
		s.Close()
	}
}

// processFileRequest handles incoming file requests.
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

// sendFile sends a file to all peers.
func sendFile(h host.Host, filename string) {
	file, err := os.Open(filename)
	if err != nil {
		fmt.Println("[⚠️] Error opening file.")
		return
	}
	defer file.Close()
	baseName := filepath.Base(filename)
	for _, p := range h.Peerstore().Peers() {
		if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
			fmt.Printf("Sending file: %s\n", baseName)
			s.Write([]byte("file:" + baseName + "\n"))
			io.Copy(s, file)
			s.Close()
			file.Seek(0, 0)
		}
	}
}

// listPeers prints all connected peers.
func listPeers(h host.Host) {
	fmt.Println("Connected peers:")
	for _, p := range h.Peerstore().Peers() {
		if len(h.Peerstore().Addrs(p)) > 0 {
			fmt.Println("-", p.ShortString())
		}
	}
}
