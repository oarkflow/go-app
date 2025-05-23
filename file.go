// main.go
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	peer "github.com/libp2p/go-libp2p/core/peer"
	disc "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/pion/webrtc/v3"
)

const (
	signalTopicPrefix = "p2p-file-share-"
	rendezvousPrefix  = "p2p-fileshare:"
)

// Signal is the JSON payload we PubSub for SDP+ICE
type Signal struct {
	Type string   `json:"type"` // "offer" or "answer"
	SDP  string   `json:"sdp"`
	ICE  []string `json:"ice"`
}

func main() {
	mode := flag.String("mode", "host", "host or join")
	filePath := flag.String("file", "", "file to share (host mode)")
	password := flag.String("pass", "", "access password (host mode)")
	code := flag.String("code", "", "share code (join mode)")
	flag.Parse()

	switch *mode {
	case "host":
		if *filePath == "" {
			log.Fatal("host mode requires -file")
		}
		hostMode(*filePath, *password)
	case "join":
		if *code == "" {
			log.Fatal("join mode requires -code")
		}
		joinMode(*code)
	default:
		log.Fatalf("unknown mode %q", *mode)
	}
}

func hostMode(path, pass string) {
	// 1) ensure file exists
	if _, err := os.Stat(path); err != nil {
		log.Fatalf("cannot stat file: %v", err)
	}
	filename := filepath.Base(path)
	ctx := context.Background()

	// 2) create a libp2p host listening on a fixed TCP port with NAT, relay, and auto relay support
	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/4001"),
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
	)
	if err != nil {
		log.Fatal(err)
	}

	// 3) set up the DHT, then connect to the default public bootstrap peers
	kDHT, err := dht.New(ctx, h)
	if err != nil {
		log.Fatal(err)
	}
	// connect to the built-in bootstrap peers
	for _, pi := range dht.GetDefaultBootstrapPeerAddrInfos() {
		if err := h.Connect(ctx, pi); err != nil {
			log.Printf("warning: could not connect to bootstrap %s: %v", pi.ID, err)
		}
	}
	if err := kDHT.Bootstrap(ctx); err != nil {
		log.Fatal(err)
	}

	// 4) advertise our one-time rendezvous string in the DHT
	code := generateID(6)
	rendezvous := rendezvousPrefix + code
	routing := disc.NewRoutingDiscovery(kDHT)

	if _, err := routing.Advertise(ctx, rendezvous); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Share this code with your peer:", code)

	// 5) set up FloodSub on our libp2p host
	ps, err := pubsub.NewFloodSub(ctx, h)
	if err != nil {
		log.Fatal(err)
	}
	topicName := signalTopicPrefix + code
	topic, err := ps.Join(topicName)
	if err != nil {
		log.Fatal(err)
	}
	sub, err := topic.Subscribe()
	if err != nil {
		log.Fatal(err)
	}

	// 6) build WebRTC PeerConnection + data channel
	pc, dc, iceCh := newPeerConnection()
	var ourICE []string
	go func() {
		for c := range iceCh {
			ourICE = append(ourICE, c)
		}
	}()

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Fatal(err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		log.Fatal(err)
	}
	<-webrtc.GatheringCompletePromise(pc)

	// 7) publish our offer over PubSub
	publish(topic, Signal{"offer", pc.LocalDescription().SDP, ourICE})
	fmt.Println("Waiting for peer answer…")

	// 8) wait for answer
	answerCh := make(chan Signal, 1)
	go func() {
		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				continue
			}
			var s Signal
			if err := json.Unmarshal(msg.Data, &s); err != nil {
				continue
			}
			if s.Type == "answer" {
				answerCh <- s
				return
			}
		}
	}()

	select {
	case ans := <-answerCh:
		if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: ans.SDP}); err != nil {
			log.Fatal(err)
		}
		for _, c := range ans.ICE {
			pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: c})
		}
		fmt.Println("Peer connected! Awaiting file request…")
	case <-time.After(5 * time.Minute):
		log.Fatal("timed out waiting for answer")
	}

	// 9) handle incoming file requests
	dc.OnOpen(func() {
		log.Println("→ Data channel open")
	})
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		msg := string(m.Data)
		if strings.HasPrefix(msg, "REQUEST:") {
			req := strings.TrimPrefix(msg, "REQUEST:")
			if pass != "" && req != pass {
				dc.SendText("ERROR: bad password")
				return
			}
			fmt.Printf("Peer requests '%s'. Approve? (y/N): ", filename)
			yn, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(yn) != "y" {
				dc.SendText("ERROR: denied")
				return
			}
			sendFile(path, dc)
		}
	})

	select {} // block forever
}

func joinMode(code string) {
	ctx := context.Background()

	// 1) create libp2p host with same options (including auto relay)
	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/4001"),
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
	)
	if err != nil {
		log.Fatal(err)
	}

	// 2) set up DHT + bootstrap
	kDHT, err := dht.New(ctx, h)
	if err != nil {
		log.Fatal(err)
	}
	for _, pi := range dht.GetDefaultBootstrapPeerAddrInfos() {
		if err := h.Connect(ctx, pi); err != nil {
			log.Printf("warning: bootstrap connect failed: %v", err)
		}
	}
	if err := kDHT.Bootstrap(ctx); err != nil {
		log.Fatal(err)
	}

	// 3) discover the host via DHT
	rendezvous := rendezvousPrefix + code
	routing := disc.NewRoutingDiscovery(kDHT)
	peerCh, err := routing.FindPeers(ctx, rendezvous)
	if err != nil {
		log.Fatal(err)
	}

	var pi peer.AddrInfo
	for {
		select {
		case p := <-peerCh:
			// Skip self and peers with no addresses
			if p.ID == h.ID() || len(p.Addrs) == 0 {
				continue
			}
			pi = p
		case <-time.After(30 * time.Second):
			log.Fatal("timed out finding peer")
		}
		if pi.ID != "" {
			break
		}
	}

	// 4) connect to host
	if err := h.Connect(ctx, pi); err != nil {
		log.Fatalf("connect failed: %v", err)
	}
	fmt.Println("Connected to host:", pi.ID)

	// 5) set up FloodSub
	ps, err := pubsub.NewFloodSub(ctx, h)
	if err != nil {
		log.Fatal(err)
	}
	topicName := signalTopicPrefix + code
	topic, err := ps.Join(topicName)
	if err != nil {
		log.Fatal(err)
	}
	sub, err := topic.Subscribe()
	if err != nil {
		log.Fatal(err)
	}

	// 6) wait for offer
	var offerSig Signal
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			continue
		}
		var s Signal
		if err := json.Unmarshal(msg.Data, &s); err != nil {
			continue
		}
		if s.Type == "offer" {
			offerSig = s
			break
		}
	}

	// 7) build PeerConnection
	pc, dc, iceCh := newPeerConnection()
	var ourICE []string
	go func() {
		for c := range iceCh {
			ourICE = append(ourICE, c)
		}
	}()

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSig.SDP}); err != nil {
		log.Fatal(err)
	}
	for _, c := range offerSig.ICE {
		pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: c})
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		log.Fatal(err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		log.Fatal(err)
	}
	<-webrtc.GatheringCompletePromise(pc)

	// 8) publish answer
	publish(topic, Signal{"answer", pc.LocalDescription().SDP, ourICE})

	// 9) request file & receive
	dc.OnOpen(func() {
		p := prompt("Password (or empty): ")
		dc.SendText("REQUEST:" + p)
	})

	var out *os.File
	gotHeader := false
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		if !gotHeader {
			hdr := string(m.Data)
			if strings.HasPrefix(hdr, "FILE:") {
				name := "downloaded_" + strings.TrimPrefix(hdr, "FILE:")
				f, err := os.Create(name)
				if err != nil {
					log.Fatal(err)
				}
				out = f
				gotHeader = true
				fmt.Println("Receiving… saving to", name)
				return
			}
		}
		if string(m.Data) == "EOF" {
			fmt.Println("Transfer complete.")
			out.Close()
			return
		}
		if gotHeader {
			if _, err := out.Write(m.Data); err != nil {
				log.Println("write error:", err)
			}
		}
	})

	select {} // block forever
}

func newPeerConnection() (*webrtc.PeerConnection, *webrtc.DataChannel, chan string) {
	// Update ICE servers with valid TURN credentials if needed for NAT traversal
	cfg := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
			// Replace TURN.SERVER.HERE, "user", "pass" with real TURN server details.
			{URLs: []string{"turn:your.turnserver.com:3478"}, Username: "youruser", Credential: "yourpass"},
		},
	}
	pc, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		log.Fatal(err)
	}
	iceCh := make(chan string, 16)
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			close(iceCh)
		} else {
			iceCh <- c.ToJSON().Candidate
		}
	})
	dc, err := pc.CreateDataChannel("file", nil)
	if err != nil {
		log.Fatal(err)
	}
	return pc, dc, iceCh
}

func publish(topic *pubsub.Topic, sig Signal) {
	b, _ := json.Marshal(sig)
	if err := topic.Publish(context.Background(), b); err != nil {
		log.Println("publish error:", err)
	}
}

func generateID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func prompt(msg string) string {
	fmt.Print(msg)
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(s)
}

func sendFile(path string, dc *webrtc.DataChannel) {
	dc.SendText("FILE:" + filepath.Base(path))
	f, err := os.Open(path)
	if err != nil {
		dc.SendText("ERROR: open failed")
		return
	}
	defer f.Close()

	buf := make([]byte, 16<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			dc.Send(buf[:n])
		}
		if err == io.EOF {
			break
		} else if err != nil {
			dc.SendText("ERROR: read failed")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	dc.SendText("EOF")
}
