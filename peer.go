package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"math/big"
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

var (
	username    = "Anonymous"
	currentRoom = "public"
	rooms       = map[string]bool{"public": true}
	adminRooms  = map[string]bool{"public": false}

	roomCodes = make(map[string]string)

	localPeerID string
)

type fileShare struct {
	filename string
	filepath string
	sender   string
}

var (
	fileShares = make(map[string]fileShare)
)

var (
	inputChan       = make(chan string)
	fileRequestChan = make(chan fileRequest)
	currentDir      = "./files"
	mu              sync.Mutex
)

type fileRequest struct {
	stream   network.Stream
	fileName string
}

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

	localPeerID = h.ID().ShortString()
	fmt.Printf("[🔗] Peer ID: %s\n", localPeerID)
	for _, addr := range h.Addrs() {
		fmt.Printf("[🌐] Listening on: %s/p2p/%s\n", addr, h.ID())
	}

	os.MkdirAll(currentDir, os.ModePerm)

	mdnsService := mdns.NewMdnsService(h, Rendezvous, &discoveryNotifee{h: h, ctx: ctx})
	if err := mdnsService.Start(); err != nil {
		log.Fatal(err)
	}

	h.SetStreamHandler(ProtocolID, handleStream(h))

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

func processCommand(line string, h host.Host) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	switch {
	case strings.HasPrefix(line, "/username "):
		newName := strings.TrimSpace(strings.TrimPrefix(line, "/username "))
		username = newName
		fmt.Printf("Username changed to %s\n", username)

	case strings.HasPrefix(line, "/room "):

		room := strings.TrimSpace(strings.TrimPrefix(line, "/room "))
		if _, ok := rooms[room]; ok {
			currentRoom = room
			fmt.Printf("Switched to room %s\n", room)
		} else {
			fmt.Printf("You are not a member of room %s. Use /joinroom <code> or /createroom.\n", room)
		}

	case strings.HasPrefix(line, "/createroom "):

		room := strings.TrimSpace(strings.TrimPrefix(line, "/createroom "))
		rooms[room] = true
		adminRooms[room] = true
		currentRoom = room
		code := generateCode(6)
		roomCodes[code] = room
		fmt.Printf("Room '%s' created. Unique room code: %s (share this code to invite others)\n", room, code)

		sendBroadcast(h, fmt.Sprintf("roomcode:%s:%s:%s\n", code, room, username))

	case strings.HasPrefix(line, "/joinroom "):

		code := strings.TrimSpace(strings.TrimPrefix(line, "/joinroom "))
		room, ok := roomCodes[code]
		if !ok {
			fmt.Printf("No room found for code %s\n", code)
		} else {
			rooms[room] = true
			currentRoom = room
			fmt.Printf("Joined room %s\n", room)
			sendBroadcast(h, fmt.Sprintf("join:%s:%s\n", room, username))
		}

	case strings.HasPrefix(line, "/invite "):

		parts := strings.Fields(line)
		if len(parts) < 3 {
			fmt.Println("Usage: /invite <roomName> <peerID>")
		} else {
			room := parts[1]
			target := parts[2]
			if admin, ok := adminRooms[room]; !ok || !admin {
				fmt.Println("You're not admin of room", room)
			} else {
				sendToPeer(h, target, fmt.Sprintf("invite:%s:%s\n", room, username))
				fmt.Printf("Invite sent to %s for room %s\n", target, room)
			}
		}

	case strings.HasPrefix(line, "/kick "):

		parts := strings.Fields(line)
		if len(parts) < 3 {
			fmt.Println("Usage: /kick <roomName> <peerID>")
		} else {
			room := parts[1]
			target := parts[2]
			if admin, ok := adminRooms[room]; !ok || !admin {
				fmt.Println("You're not admin of room", room)
			} else {
				sendBroadcast(h, fmt.Sprintf("kick:%s:%s:%s\n", room, target, username))
				fmt.Printf("Kick message sent for %s from room %s\n", target, room)
			}
		}

	case strings.HasPrefix(line, "/msg "):

		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			fmt.Println("Usage: /msg <peerID> <message>")
		} else {
			target := parts[1]
			msg := parts[2]
			sendToPeer(h, target, fmt.Sprintf("private:%s:%s:%s\n", target, username, msg))
		}

	case strings.HasPrefix(line, "/chat "):

		msg := strings.TrimSpace(strings.TrimPrefix(line, "/chat "))
		if currentRoom == "public" {
			sendBroadcast(h, fmt.Sprintf("chat:public:%s:%s\n", username, msg))
		} else {
			sendBroadcast(h, fmt.Sprintf("chat:group:%s:%s:%s\n", currentRoom, username, msg))
		}

	case strings.HasPrefix(line, "/sendfile "):

		filename := strings.TrimSpace(strings.TrimPrefix(line, "/sendfile "))
		sendFile(h, filename)

	case strings.HasPrefix(line, "/sendfilecode "):

		filepathArg := strings.TrimSpace(strings.TrimPrefix(line, "/sendfilecode "))
		sendFileCode(h, filepathArg)

	case strings.HasPrefix(line, "/getfile "):

		code := strings.TrimSpace(strings.TrimPrefix(line, "/getfile "))
		sendBroadcast(h, fmt.Sprintf("getfile:%s:%s\n", code, localPeerID))

	case strings.HasPrefix(line, "/setdir "):

		dir := strings.TrimSpace(strings.TrimPrefix(line, "/setdir "))
		mu.Lock()
		currentDir = dir
		mu.Unlock()
		fmt.Printf("Save directory set to: %s\n", currentDir)

	case line == "/peers":
		listPeers(h)

	case line == "/exit":
		fmt.Println("Exiting...")
		os.Exit(0)

	default:
		fmt.Println("Invalid command.")
	}
}

func sendBroadcast(h host.Host, msg string) {
	for _, p := range h.Peerstore().Peers() {
		if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
			s.Write([]byte(msg))
			s.Close()
		}
	}
}

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

func generateCode(length int) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, length)
	for i := range result {
		nBig, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			result[i] = charset[0]
		} else {
			result[i] = charset[nBig.Int64()]
		}
	}
	return string(result)
}

func handleStream(h host.Host) func(network.Stream) {
	return func(s network.Stream) {
		reader := bufio.NewReader(s)
		data, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("[⚠️] Error reading stream:", err)
			s.Close()
			return
		}
		data = strings.TrimSpace(data)
		switch {

		case strings.HasPrefix(data, "chat:"):

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
				if _, ok := rooms[roomName]; ok {
					fmt.Printf("[Group:%s] %s: %s\n", roomName, parts[3], parts[4])
				}
			}
			s.Close()

		case strings.HasPrefix(data, "private:"):

			parts := strings.SplitN(data, ":", 4)
			if len(parts) < 4 {
				fmt.Println("[⚠️] Invalid private message format.")
				s.Close()
				return
			}
			fmt.Printf("[Private] %s: %s\n", parts[2], parts[3])
			s.Close()

		case strings.HasPrefix(data, "invite:"):

			parts := strings.SplitN(data, ":", 3)
			if len(parts) < 3 {
				fmt.Println("[⚠️] Invalid invite message format.")
				s.Close()
				return
			}
			roomName := parts[1]
			inviter := parts[2]
			fmt.Printf("[Invite] %s invited you to join room '%s'. To join, type: /joinroom <roomCode>\n", inviter, roomName)
			s.Close()

		case strings.HasPrefix(data, "roomcode:"):

			parts := strings.SplitN(data, ":", 4)
			if len(parts) < 4 {
				fmt.Println("[⚠️] Invalid roomcode message.")
				s.Close()
				return
			}
			code := parts[1]
			roomName := parts[2]

			roomCodes[code] = roomName
			fmt.Printf("[Room Code] Room '%s' is available with code: %s (created by %s)\n", roomName, code, parts[3])
			s.Close()

		case strings.HasPrefix(data, "join:"):

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

		case strings.HasPrefix(data, "kick:"):

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
			if target == localPeerID {
				delete(rooms, roomName)
				if currentRoom == roomName {
					currentRoom = "public"
				}
				fmt.Printf("You have been removed from room %s. Switched to public room.\n", roomName)
			}
			s.Close()

		case strings.HasPrefix(data, "file:"):

			parts := strings.SplitN(data, ":", 2)
			if len(parts) < 2 {
				fmt.Println("[⚠️] Invalid file request received.")
				s.Close()
				return
			}
			fName := strings.TrimSpace(parts[1])
			fileRequestChan <- fileRequest{stream: s, fileName: fName}

		case strings.HasPrefix(data, "filecode:"):

			parts := strings.SplitN(data, ":", 4)
			if len(parts) < 4 {
				fmt.Println("[⚠️] Invalid filecode message.")
				s.Close()
				return
			}
			code := parts[1]
			fName := parts[2]
			sender := parts[3]
			fmt.Printf("[File Share] File '%s' is available with code: %s from %s. Use /getfile %s to request it.\n", fName, code, sender, code)
			s.Close()

		case strings.HasPrefix(data, "getfile:"):

			parts := strings.SplitN(data, ":", 3)
			if len(parts) < 3 {
				fmt.Println("[⚠️] Invalid getfile message.")
				s.Close()
				return
			}
			code := parts[1]
			receiver := parts[2]

			share, ok := fileShares[code]
			if !ok {
				fmt.Printf("[⚠️] No file share found for code %s\n", code)
				s.Close()
				return
			}
			if share.sender != localPeerID {

				s.Close()
				return
			}

			sendFileForCode(h, receiver, share)
			s.Close()

		case strings.HasPrefix(data, "instantfile:"):

			parts := strings.SplitN(data, ":", 2)
			if len(parts) < 2 {
				fmt.Println("[⚠️] Invalid instant file header.")
				s.Close()
				return
			}
			fName := strings.TrimSpace(parts[1])
			savePath := filepath.Join(currentDir, fName)
			file, err := os.Create(savePath)
			if err != nil {
				fmt.Printf("[⚠️] Error creating file: %v\n", err)
				s.Close()
				return
			}
			io.Copy(file, s)
			file.Close()
			s.Close()
			fmt.Printf("Instant file received: %s\n", savePath)

		default:
			fmt.Printf("[⚠️] Unknown message type: %s\n", data)
			s.Close()
		}
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
		saveDir = currentDir
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

func sendFileCode(h host.Host, filepathArg string) {
	file, err := os.Open(filepathArg)
	if err != nil {
		fmt.Println("[⚠️] Error opening file.")
		return
	}
	defer file.Close()
	baseName := filepath.Base(filepathArg)
	code := generateCode(6)

	fileShares[code] = fileShare{
		filename: baseName,
		filepath: filepathArg,
		sender:   localPeerID,
	}
	fmt.Printf("File sharing code for '%s': %s (share this code with recipients)\n", baseName, code)

	sendBroadcast(h, fmt.Sprintf("filecode:%s:%s:%s\n", code, baseName, localPeerID))
}

func sendFileForCode(h host.Host, target string, share fileShare) {
	for _, p := range h.Peerstore().Peers() {
		if p.ShortString() == target {
			s, err := h.NewStream(context.Background(), p, ProtocolID)
			if err != nil {
				fmt.Printf("Error creating stream for file transfer: %v\n", err)
				return
			}

			s.Write([]byte("instantfile:" + share.filename + "\n"))
			f, err := os.Open(share.filepath)
			if err != nil {
				fmt.Printf("Error opening file for code transfer: %v\n", err)
				s.Close()
				return
			}
			defer f.Close()
			io.Copy(s, f)
			s.Close()
			fmt.Printf("File '%s' sent to peer %s\n", share.filename, target)
			return
		}
	}
	fmt.Printf("Peer %s not found for file transfer.\n", target)
}

func listPeers(h host.Host) {
	fmt.Println("Connected peers:")
	for _, p := range h.Peerstore().Peers() {
		if len(h.Peerstore().Addrs(p)) > 0 {
			fmt.Println("-", p.ShortString())
		}
	}
}
