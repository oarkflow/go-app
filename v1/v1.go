package main

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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

	"github.com/pion/webrtc/v3"
)

// Protocol and rendezvous parameters
const (
	ProtocolID = "/p2p-chat-file/1.0.0"
	Rendezvous = "p2p-chat-file-rendezvous"
)

// Hard-coded secret used for encrypting/decrypting SDP (for demonstration)
const fixedSecret = "MY_SECRET_KEY"

// ------------------ Global State ------------------
var (
	// Chat/room and file sharing state
	username        = "Anonymous"
	currentRoom     = "public"
	rooms           = map[string]bool{"public": true}
	adminRooms      = map[string]bool{"public": false}
	roomCodes       = make(map[string]string) // room join codes (kept private)
	localPeerID     string
	fileShares      = make(map[string]fileShare) // file share info keyed by file code
	inputChan       = make(chan string)
	fileRequestChan = make(chan fileRequest)
	currentDir      = "./files"
	mu              sync.Mutex

	// WebRTC negotiation state
	// pendingOffer holds the offer info when this peer creates an offer.
	pendingOffer *webrtcOfferInfo
	webrtcMu     sync.Mutex
)

// ------------------ Structures ------------------
type fileShare struct {
	filename string
	filepath string
	sender   string
}

type fileRequest struct {
	stream   network.Stream
	fileName string
}

// webrtcOfferInfo holds our offer (if we create one) for later finalization.
type webrtcOfferInfo struct {
	sdp string
	pc  *webrtc.PeerConnection
	dc  *webrtc.DataChannel
	// secret is the (hard-coded) secret used for encryption/decryption.
	secret string
	// offerCode is the URL-friendly encrypted SDP that is printed.
	offerCode string
}

// ------------------ MDNS Discovery ------------------
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

// ------------------ Main ------------------
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
	_ = os.MkdirAll(currentDir, os.ModePerm)
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

// ------------------ Command Processing ------------------
func processCommand(line string, h host.Host) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	switch {
	// Change username
	case strings.HasPrefix(line, "/username "):
		newName := strings.TrimSpace(strings.TrimPrefix(line, "/username "))
		username = newName
		fmt.Printf("Username changed to %s\n", username)

	// Switch room (only if member)
	case strings.HasPrefix(line, "/room "):
		room := strings.TrimSpace(strings.TrimPrefix(line, "/room "))
		if _, ok := rooms[room]; ok {
			currentRoom = room
			fmt.Printf("Switched to room %s\n", room)
		} else {
			fmt.Printf("You are not a member of room %s. Use /joinroom <code> or /createroom.\n", room)
		}

	// Create room (you become admin) – room join code is generated but not broadcast
	case strings.HasPrefix(line, "/createroom "):
		room := strings.TrimSpace(strings.TrimPrefix(line, "/createroom "))
		rooms[room] = true
		adminRooms[room] = true
		currentRoom = room
		code := generateCode(6)
		roomCodes[code] = room
		fmt.Printf("Room '%s' created. Unique room code: %s (share this code privately to invite others)\n", room, code)

	// Join room via secret code (code is not broadcast)
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

	// Invite a peer to a room (only admin)
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

	// Kick a peer from a room (only admin)
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

	// Private message
	case strings.HasPrefix(line, "/msg "):
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			fmt.Println("Usage: /msg <peerID> <message>")
		} else {
			target := parts[1]
			msg := parts[2]
			sendToPeer(h, target, fmt.Sprintf("private:%s:%s:%s\n", target, username, msg))
		}

	// Chat to room (public if "public"; group otherwise)
	case strings.HasPrefix(line, "/chat "):
		msg := strings.TrimSpace(strings.TrimPrefix(line, "/chat "))
		if currentRoom == "public" {
			sendBroadcast(h, fmt.Sprintf("chat:public:%s:%s\n", username, msg))
		} else {
			sendBroadcast(h, fmt.Sprintf("chat:group:%s:%s:%s\n", currentRoom, username, msg))
		}

	// File transfer: direct send (streamed to all peers)
	case strings.HasPrefix(line, "/sendfile "):
		filename := strings.TrimSpace(strings.TrimPrefix(line, "/sendfile "))
		sendFile(h, filename)

	// File sharing via code
	case strings.HasPrefix(line, "/sendfilecode "):
		filepathArg := strings.TrimSpace(strings.TrimPrefix(line, "/sendfilecode "))
		sendFileCode(filepathArg)

	// Request a file via its share code
	case strings.HasPrefix(line, "/getfile "):
		code := strings.TrimSpace(strings.TrimPrefix(line, "/getfile "))
		sendBroadcast(h, fmt.Sprintf("getfile:%s:%s\n", code, localPeerID))

	// Set local file save directory
	case strings.HasPrefix(line, "/setdir "):
		dir := strings.TrimSpace(strings.TrimPrefix(line, "/setdir "))
		mu.Lock()
		currentDir = dir
		mu.Unlock()
		fmt.Printf("Save directory set to: %s\n", currentDir)

	// List connected peers
	case line == "/peers":
		listPeers(h)

	// Exit the application
	case line == "/exit":
		fmt.Println("Exiting...")
		os.Exit(0)

	// ------------------ WebRTC Negotiation Commands ------------------
	// Create a WebRTC offer. (No target peer; the SDP offer is encrypted as a URL‑friendly value.)
	// Usage: /webrtc-offer
	case line == "/webrtc-offer":
		webrtcOffer(h)

	// Accept an offer by providing the offer code.
	// Usage: /webrtc-accept <offerCode>
	case strings.HasPrefix(line, "/webrtc-accept "):
		parts := strings.Fields(line)
		if len(parts) < 2 {
			fmt.Println("Usage: /webrtc-accept <offerCode>")
		} else {
			offerCode := parts[1]
			webrtcAccept(offerCode, h)
		}

	// Finalize the offer by providing the answer code.
	// Usage: /webrtc-finalize <answerCode>
	case strings.HasPrefix(line, "/webrtc-finalize "):
		parts := strings.Fields(line)
		if len(parts) < 2 {
			fmt.Println("Usage: /webrtc-finalize <answerCode>")
		} else {
			answerCode := parts[1]
			webrtcFinalize(answerCode)
		}

	default:
		fmt.Println("Invalid command.")
	}
}

func sendBroadcast(h host.Host, msg string) {
	for _, p := range h.Peerstore().Peers() {
		if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
			_, _ = s.Write([]byte(msg))
			s.Close()
		}
	}
}

func sendToPeer(h host.Host, target string, msg string) {
	for _, p := range h.Peerstore().Peers() {
		if p.ShortString() == target {
			if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
				_, _ = s.Write([]byte(msg))
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

// ------------------ Stream Handler ------------------
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
		// (The normal chat, room, and file message handling remains unchanged.)
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
			_, _ = io.Copy(file, s)
			file.Close()
			s.Close()
			fmt.Printf("Instant file received: %s\n", savePath)
		// (For simplicity, the legacy WebRTC message types are not handled here.)
		default:
			fmt.Printf("[⚠️] Unknown message type: %s\n", data)
			s.Close()
		}
	}
}

// ------------------ File Transfer Functions ------------------
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
			_, _ = s.Write([]byte("file:" + baseName + "\n"))
			_, _ = io.Copy(s, file)
			s.Close()
			file.Seek(0, 0)
		}
	}
}

func sendFileCode(filepathArg string) {
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
	fmt.Printf("File sharing code for '%s': %s (share this code privately with recipients)\n", baseName, code)
}

func sendFileForCode(h host.Host, target string, share fileShare) {
	for _, p := range h.Peerstore().Peers() {
		if p.ShortString() == target {
			s, err := h.NewStream(context.Background(), p, ProtocolID)
			if err != nil {
				fmt.Printf("Error creating stream for file transfer: %v\n", err)
				return
			}
			_, _ = s.Write([]byte("instantfile:" + share.filename + "\n"))
			f, err := os.Open(share.filepath)
			if err != nil {
				fmt.Printf("Error opening file for code transfer: %v\n", err)
				s.Close()
				return
			}
			defer f.Close()
			_, _ = io.Copy(s, f)
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

// ------------------ Encryption Utilities ------------------
// deriveKey returns a 16-byte AES-128 key derived from the secret using SHA-256.
func deriveKey(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return hash[:16]
}

// encrypt encrypts plaintext using AES-GCM and returns a URL-friendly Base64 string.
func encrypt(plaintext, secret string) (string, error) {
	key := deriveKey(secret)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	// Use URL-friendly encoding.
	return base64.URLEncoding.EncodeToString(ciphertext), nil
}

// decrypt decrypts a URL-friendly Base64 string using AES-GCM.
func decrypt(ciphertextB64, secret string) (string, error) {
	key := deriveKey(secret)
	ciphertext, err := base64.URLEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// ------------------ WebRTC Negotiation Functions ------------------

// webrtcOffer creates a PeerConnection and DataChannel, creates an SDP offer,
// encrypts the SDP using the fixed secret, saves the offer info for later finalization,
// and prints the URL-friendly encrypted offer code.
func webrtcOffer(h host.Host) {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	}
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		fmt.Println("[WebRTC] Error creating PeerConnection:", err)
		return
	}
	dc, err := pc.CreateDataChannel("data", nil)
	if err != nil {
		fmt.Println("[WebRTC] Error creating DataChannel:", err)
		return
	}
	dc.OnOpen(func() {
		fmt.Println("[WebRTC] Data channel opened!")
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		fmt.Printf("[WebRTC] Message on data channel: %s\n", string(msg.Data))
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		fmt.Println("[WebRTC] Error creating offer:", err)
		return
	}
	err = pc.SetLocalDescription(offer)
	if err != nil {
		fmt.Println("[WebRTC] Error setting local description:", err)
		return
	}
	<-webrtc.GatheringCompletePromise(pc)
	sdpOffer := pc.LocalDescription().SDP

	encryptedOffer, err := encrypt(sdpOffer, fixedSecret)
	if err != nil {
		fmt.Println("[WebRTC] Error encrypting offer:", err)
		return
	}
	webrtcMu.Lock()
	pendingOffer = &webrtcOfferInfo{
		sdp:       sdpOffer,
		pc:        pc,
		dc:        dc,
		secret:    fixedSecret,
		offerCode: encryptedOffer,
	}
	webrtcMu.Unlock()
	fmt.Println("[WebRTC] Offer created.")
	fmt.Printf("Copy and share this Offer Code (URL-friendly):\n%s\n", encryptedOffer)
}

// webrtcAccept takes an offer code (encrypted SDP offer), decrypts it using the fixed secret,
// creates an answer, encrypts the answer SDP with the same secret, and prints the answer code.
func webrtcAccept(offerCode string, h host.Host) {
	sdpOffer, err := decrypt(offerCode, fixedSecret)
	if err != nil {
		fmt.Println("[WebRTC] Error decrypting offer. Verify that you copied the correct Offer Code and that the secret is correct.")
		return
	}
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	}
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		fmt.Println("[WebRTC] Error creating PeerConnection (answerer):", err)
		return
	}
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		fmt.Printf("[WebRTC] Data channel '%s'-[%d] opened on answerer side.\n", dc.Label(), dc.ID())
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			fmt.Printf("[WebRTC] Message on data channel (answerer): %s\n", string(msg.Data))
		})
	})
	offerDesc := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdpOffer,
	}
	err = pc.SetRemoteDescription(offerDesc)
	if err != nil {
		fmt.Println("[WebRTC] Error setting remote description on answerer:", err)
		return
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		fmt.Println("[WebRTC] Error creating answer:", err)
		return
	}
	err = pc.SetLocalDescription(answer)
	if err != nil {
		fmt.Println("[WebRTC] Error setting local description on answerer:", err)
		return
	}
	<-webrtc.GatheringCompletePromise(pc)
	sdpAnswer := pc.LocalDescription().SDP
	encryptedAnswer, err := encrypt(sdpAnswer, fixedSecret)
	if err != nil {
		fmt.Println("[WebRTC] Error encrypting answer:", err)
		return
	}
	fmt.Println("[WebRTC] Answer created.")
	fmt.Printf("Copy and share this Answer Code (URL-friendly) with the offerer:\n%s\n", encryptedAnswer)
}

// webrtcFinalize is run by the offerer to finalize the connection by processing the Answer Code.
func webrtcFinalize(answerCode string) {
	webrtcMu.Lock()
	defer webrtcMu.Unlock()
	if pendingOffer == nil {
		fmt.Println("[WebRTC] No pending offer exists. Did you run /webrtc-offer?")
		return
	}
	sdpAnswer, err := decrypt(answerCode, pendingOffer.secret)
	if err != nil {
		fmt.Println("[WebRTC] Error decrypting answer. Verify that you copied the correct Answer Code and that the secret is correct.")
		return
	}
	answerDesc := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sdpAnswer,
	}
	err = pendingOffer.pc.SetRemoteDescription(answerDesc)
	if err != nil {
		fmt.Println("[WebRTC] Error setting remote description on offerer:", err)
		return
	}
	fmt.Println("[WebRTC] WebRTC connection established!")
	pendingOffer = nil
}
