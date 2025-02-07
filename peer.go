package main

import (
	"bufio"
	"context"
	"crypto/rand"
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

const (
	ProtocolID = "/p2p-chat-file/1.0.0"
	Rendezvous = "p2p-chat-file-rendezvous"
)

type fileShare struct {
	filename string
	filepath string
	sender   string
}

var (
	username        = "Anonymous"
	currentRoom     = "public"
	rooms           = map[string]bool{"public": true}
	adminRooms      = map[string]bool{"public": false}
	roomCodes       = make(map[string]string)
	localPeerID     string
	fileShares      = make(map[string]fileShare)
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
	defer func() {
		_ = h.Close()
	}()
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
		fmt.Printf("Room '%s' created. Unique room code: %s (share this code privately to invite others)\n", room, code)
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
		sendFileCode(filepathArg)
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
	case line == "/webrtc-offer":
		initiateWebRTCOffer(h)
	case strings.HasPrefix(line, "/webrtc-accept "):
		code := strings.TrimSpace(strings.TrimPrefix(line, "/webrtc-accept "))
		acceptWebRTCOffer(code, h)
	default:
		fmt.Println("Invalid command.")
	}
}

func sendBroadcast(h host.Host, msg string) {
	for _, p := range h.Peerstore().Peers() {
		if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
			_, _ = s.Write([]byte(msg))
			_ = s.Close()
		}
	}
}

func sendToPeer(h host.Host, target string, msg string) {
	for _, p := range h.Peerstore().Peers() {
		if p.ShortString() == target {
			if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
				_, _ = s.Write([]byte(msg))
				_ = s.Close()
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
			_ = s.Close()
			return
		}
		data = strings.TrimSpace(data)

		switch {
		case strings.HasPrefix(data, "chat:"):
			parts := strings.SplitN(data, ":", 5)
			if len(parts) < 4 {
				fmt.Println("[⚠️] Invalid chat message format.")
				_ = s.Close()
				return
			}
			if parts[1] == "public" {
				fmt.Printf("[Public] %s: %s\n", parts[2], parts[3])
			} else if parts[1] == "group" {
				if len(parts) < 5 {
					fmt.Println("[⚠️] Invalid group chat format.")
					_ = s.Close()
					return
				}
				roomName := parts[2]
				if _, ok := rooms[roomName]; ok {
					fmt.Printf("[Group:%s] %s: %s\n", roomName, parts[3], parts[4])
				}
			}
			_ = s.Close()
		case strings.HasPrefix(data, "private:"):
			parts := strings.SplitN(data, ":", 4)
			if len(parts) < 4 {
				fmt.Println("[⚠️] Invalid private message format.")
				_ = s.Close()
				return
			}
			fmt.Printf("[Private] %s: %s\n", parts[2], parts[3])
			_ = s.Close()
		case strings.HasPrefix(data, "invite:"):
			parts := strings.SplitN(data, ":", 3)
			if len(parts) < 3 {
				fmt.Println("[⚠️] Invalid invite message format.")
				_ = s.Close()
				return
			}
			roomName := parts[1]
			inviter := parts[2]
			fmt.Printf("[Invite] %s invited you to join room '%s'. To join, type: /joinroom <roomCode>\n", inviter, roomName)
			_ = s.Close()
		case strings.HasPrefix(data, "roomcode:"):
			parts := strings.SplitN(data, ":", 4)
			if len(parts) < 4 {
				fmt.Println("[⚠️] Invalid roomcode message.")
				_ = s.Close()
				return
			}
			code := parts[1]
			roomName := parts[2]
			roomCodes[code] = roomName
			fmt.Printf("[Room Code] Room '%s' is available with code: %s (created by %s)\n", roomName, code, parts[3])
			_ = s.Close()
		case strings.HasPrefix(data, "join:"):
			parts := strings.SplitN(data, ":", 3)
			if len(parts) < 3 {
				fmt.Println("[⚠️] Invalid join message format.")
				_ = s.Close()
				return
			}
			roomName := parts[1]
			joiner := parts[2]
			fmt.Printf("[Room %s] %s has joined.\n", roomName, joiner)
			_ = s.Close()
		case strings.HasPrefix(data, "kick:"):
			parts := strings.SplitN(data, ":", 4)
			if len(parts) < 4 {
				fmt.Println("[⚠️] Invalid kick message format.")
				_ = s.Close()
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
			_ = s.Close()
		case strings.HasPrefix(data, "file:"):
			parts := strings.SplitN(data, ":", 2)
			if len(parts) < 2 {
				fmt.Println("[⚠️] Invalid file request received.")
				_ = s.Close()
				return
			}
			fName := strings.TrimSpace(parts[1])
			fileRequestChan <- fileRequest{stream: s, fileName: fName}
		case strings.HasPrefix(data, "filecode:"):
			parts := strings.SplitN(data, ":", 4)
			if len(parts) < 4 {
				fmt.Println("[⚠️] Invalid filecode message.")
				_ = s.Close()
				return
			}
			code := parts[1]
			fName := parts[2]
			sender := parts[3]
			fmt.Printf("[File Share] File '%s' is available with code: %s from %s. Use /getfile %s to request it.\n", fName, code, sender, code)
			_ = s.Close()
		case strings.HasPrefix(data, "getfile:"):
			parts := strings.SplitN(data, ":", 3)
			if len(parts) < 3 {
				fmt.Println("[⚠️] Invalid getfile message.")
				_ = s.Close()
				return
			}
			code := parts[1]
			receiver := parts[2]
			share, ok := fileShares[code]
			if !ok {
				fmt.Printf("[⚠️] No file share found for code %s\n", code)
				_ = s.Close()
				return
			}
			if share.sender != localPeerID {
				_ = s.Close()
				return
			}
			sendFileForCode(h, receiver, share)
			_ = s.Close()
		case strings.HasPrefix(data, "instantfile:"):
			parts := strings.SplitN(data, ":", 2)
			if len(parts) < 2 {
				fmt.Println("[⚠️] Invalid instant file header.")
				_ = s.Close()
				return
			}
			fName := strings.TrimSpace(parts[1])
			savePath := filepath.Join(currentDir, fName)
			file, err := os.Create(savePath)
			if err != nil {
				fmt.Printf("[⚠️] Error creating file: %v\n", err)
				_ = s.Close()
				return
			}
			_, _ = io.Copy(file, s)
			_ = file.Close()
			_ = s.Close()
			fmt.Printf("Instant file received: %s\n", savePath)
		case strings.HasPrefix(data, "sdp-offer-code:"):
			parts := strings.SplitN(data, ":", 3)
			if len(parts) < 3 {
				fmt.Println("[WebRTC] Invalid sdp-offer-code message.")
				_ = s.Close()
				return
			}
			offerCode := parts[1]
			offererID := parts[2]

			pendingOfferCodes[offerCode] = offererID
			fmt.Printf("[WebRTC] Received offer code %s from %s\n", offerCode, offererID)
			_ = s.Close()
		case strings.HasPrefix(data, "sdp-request:"):

			parts := strings.SplitN(data, ":", 3)
			if len(parts) < 3 {
				fmt.Println("[WebRTC] Invalid sdp-request message.")
				_ = s.Close()
				return
			}
			offerCode := parts[1]
			requesterID := parts[2]
			processSDPRequest(offerCode, requesterID, h)
			_ = s.Close()
		case strings.HasPrefix(data, "sdp-offer-full:"):
			parts := strings.SplitN(data, ":", 3)
			if len(parts) < 3 {
				fmt.Println("[WebRTC] Invalid sdp-offer-full message.")
				_ = s.Close()
				return
			}
			offerCode := parts[1]
			encodedOffer := parts[2]
			processSDPOfferFull(offerCode, encodedOffer, h)
			_ = s.Close()
		case strings.HasPrefix(data, "sdp-answer-code:"):
			parts := strings.SplitN(data, ":", 4)
			if len(parts) < 4 {
				fmt.Println("[WebRTC] Invalid sdp-answer-code message.")
				_ = s.Close()
				return
			}
			offerCode := parts[1]
			answerCode := parts[2]
			answererID := parts[3]
			processSDPAnswerCode(offerCode, answerCode, answererID, h)
			_ = s.Close()
		case strings.HasPrefix(data, "sdp-request-answer:"):
			parts := strings.SplitN(data, ":", 3)
			if len(parts) < 3 {
				fmt.Println("[WebRTC] Invalid sdp-request-answer message.")
				_ = s.Close()
				return
			}
			answerCode := parts[1]
			requesterID := parts[2]
			processSDPRequestAnswer(answerCode, requesterID, h)
			_ = s.Close()
		case strings.HasPrefix(data, "sdp-answer-full:"):
			parts := strings.SplitN(data, ":", 3)
			if len(parts) < 3 {
				fmt.Println("[WebRTC] Invalid sdp-answer-full message.")
				_ = s.Close()
				return
			}
			answerCode := parts[1]
			encodedAnswer := parts[2]
			processSDPAnswerFull(answerCode, encodedAnswer)
			_ = s.Close()
		default:
			fmt.Printf("[⚠️] Unknown message type: %s\n", data)
			_ = s.Close()
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
		_ = req.stream.Close()
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
		_ = req.stream.Close()
		return
	}
	fmt.Printf("[📂] Receiving file: %s\n", savePath)
	_, err = io.Copy(file, req.stream)
	if err != nil {
		fmt.Printf("[⚠️] Error receiving file: %v\n", err)
		_ = file.Close()
		_ = req.stream.Close()
		return
	}
	_ = file.Close()
	_ = req.stream.Close()
	fmt.Printf("[✅] File received: %s\n", savePath)
}

func sendFile(h host.Host, filename string) {
	file, err := os.Open(filename)
	if err != nil {
		fmt.Println("[⚠️] Error opening file.")
		return
	}
	defer func() {
		_ = file.Close()
	}()
	baseName := filepath.Base(filename)
	for _, p := range h.Peerstore().Peers() {
		if s, err := h.NewStream(context.Background(), p, ProtocolID); err == nil {
			fmt.Printf("Sending file: %s\n", baseName)
			_, _ = s.Write([]byte("file:" + baseName + "\n"))
			_, _ = io.Copy(s, file)
			_ = s.Close()
			_, _ = file.Seek(0, 0)
		}
	}
}

func sendFileCode(filepathArg string) {
	file, err := os.Open(filepathArg)
	if err != nil {
		fmt.Println("[⚠️] Error opening file.")
		return
	}
	defer func() {
		_ = file.Close()
	}()
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
				_ = s.Close()
				return
			}
			defer func() {
				_ = f.Close()
			}()
			_, _ = io.Copy(s, f)
			_ = s.Close()
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

var pendingOffers = make(map[string]*webrtcOfferInfo)

type webrtcOfferInfo struct {
	sdp      string
	pc       *webrtc.PeerConnection
	dc       *webrtc.DataChannel
	fromPeer string
}

var pendingOfferCodes = make(map[string]string)

var pendingAnswers = make(map[string]*webrtcAnswerInfo)

type webrtcAnswerInfo struct {
	sdp      string
	pc       *webrtc.PeerConnection
	fromPeer string
}

func initiateWebRTCOffer(h host.Host) {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		fmt.Println("[WebRTC] Error creating PeerConnection:", err)
		return
	}
	dc, err := pc.CreateDataChannel("data", nil)
	if err != nil {
		fmt.Println("[WebRTC] Error creating data channel:", err)
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
	offerCode := generateCode(6)
	pendingOffers[offerCode] = &webrtcOfferInfo{
		sdp:      sdpOffer,
		pc:       pc,
		dc:       dc,
		fromPeer: localPeerID,
	}
	fmt.Printf("[WebRTC] Offer created with code: %s\n", offerCode)
	sendBroadcast(h, fmt.Sprintf("sdp-offer-code:%s:%s\n", offerCode, localPeerID))
}

func acceptWebRTCOffer(offerCode string, h host.Host) {
	offererID, ok := pendingOfferCodes[offerCode]
	if !ok {
		fmt.Printf("[WebRTC] No offer found with code %s\n", offerCode)
		return
	}
	fmt.Printf("[WebRTC] Requesting full offer SDP for code %s from %s\n", offerCode, offererID)
	sendToPeer(h, offererID, fmt.Sprintf("sdp-request:%s:%s\n", offerCode, localPeerID))
}

func processSDPRequest(offerCode, requesterID string, h host.Host) {
	offerInfo, ok := pendingOffers[offerCode]
	if !ok {
		fmt.Printf("[WebRTC] No pending offer for code %s\n", offerCode)
		return
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(offerInfo.sdp))
	msg := fmt.Sprintf("sdp-offer-full:%s:%s\n", offerCode, encoded)
	sendToPeer(h, requesterID, msg)
	fmt.Printf("[WebRTC] Sent full offer SDP for code %s to %s\n", offerCode, requesterID)
}

func processSDPOfferFull(offerCode, encodedOffer string, h host.Host) {
	decoded, err := base64.StdEncoding.DecodeString(encodedOffer)
	if err != nil {
		fmt.Println("[WebRTC] Error decoding offer SDP:", err)
		return
	}
	sdpOffer := string(decoded)
	fmt.Printf("[WebRTC] Received full offer SDP for code %s\n", offerCode)
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
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
	answerCode := generateCode(6)
	pendingAnswers[answerCode] = &webrtcAnswerInfo{
		sdp:      sdpAnswer,
		pc:       pc,
		fromPeer: localPeerID,
	}
	offererID := pendingOfferCodes[offerCode]
	fmt.Printf("[WebRTC] Answer created with code %s. Sending answer code to offerer %s\n", answerCode, offererID)
	sendToPeer(h, offererID, fmt.Sprintf("sdp-answer-code:%s:%s:%s\n", offerCode, answerCode, localPeerID))
}

func processSDPAnswerCode(offerCode, answerCode, answererID string, h host.Host) {
	fmt.Printf("[WebRTC] Received answer code %s for offer %s from %s\n", answerCode, offerCode, answererID)
	sendToPeer(h, answererID, fmt.Sprintf("sdp-request-answer:%s:%s\n", answerCode, localPeerID))
}

func processSDPRequestAnswer(answerCode, requesterID string, h host.Host) {
	answerInfo, ok := pendingAnswers[answerCode]
	if !ok {
		fmt.Printf("[WebRTC] No pending answer for code %s\n", answerCode)
		return
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(answerInfo.sdp))
	msg := fmt.Sprintf("sdp-answer-full:%s:%s\n", answerCode, encoded)
	sendToPeer(h, requesterID, msg)
	fmt.Printf("[WebRTC] Sent full answer SDP for code %s to %s\n", answerCode, requesterID)
}

func processSDPAnswerFull(answerCode, encodedAnswer string) {
	decoded, err := base64.StdEncoding.DecodeString(encodedAnswer)
	if err != nil {
		fmt.Println("[WebRTC] Error decoding answer SDP:", err)
		return
	}
	sdpAnswer := string(decoded)
	fmt.Printf("[WebRTC] Received full answer SDP for answer code %s\n", answerCode)
	for oc, info := range pendingOffers {
		answerDesc := webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  sdpAnswer,
		}
		err := info.pc.SetRemoteDescription(answerDesc)
		if err != nil {
			fmt.Println("[WebRTC] Error setting remote description on offerer:", err)
			return
		}
		fmt.Printf("[WebRTC] WebRTC connection established for offer %s!\n", oc)
		break
	}
}
