package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	maddr "github.com/multiformats/go-multiaddr"
)

var logger = logging.Logger("chat")

// Add global variable to hold our own PeerID and alias.
var selfID string
var selfAlias string

// Add global peers slice to manage active streams.
var (
	peersMu sync.Mutex
	peers   []*bufio.ReadWriter
)

func main() {
	// Disable verbose logs to beautify terminal output
	logging.SetLogLevel("*", "error")
	logging.SetLogLevel("chat", "error")

	config, err := ParseFlags()
	if err != nil {
		panic(err)
	}

	ctx := context.Background()

	// Create a persistent identity for a consistent PeerID.
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 2048)
	if err != nil {
		panic(err)
	}

	host, err := libp2p.New(
		libp2p.Identity(priv), // ensure persistent identity
		libp2p.ListenAddrs(config.ListenAddresses...),
		libp2p.Security(noise.ID, noise.New),
		// Added NAT port mapping for traversal across networks
		libp2p.NATPortMap(),
	)
	if err != nil {
		panic(err)
	}

	host.SetStreamHandler(protocol.ID(config.ProtocolID), handleStream)

	fmt.Printf("Your Peer ID: %s\n", host.ID())
	selfID = host.ID().String() // assign global selfID
	if config.Alias != "" {
		selfAlias = config.Alias
	} else {
		selfAlias = selfID
	}
	for _, addr := range host.Addrs() {
		fmt.Printf(" - %s/p2p/%s\n", addr, host.ID())
	}

	// Start mDNS
	mdnsChan := initMDNS(host, config.RendezvousString)

	// Start DHT
	kademliaDHT, err := dht.New(ctx, host)
	if err != nil {
		panic(err)
	}
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		panic(err)
	}

	routingDiscovery := drouting.NewRoutingDiscovery(kademliaDHT)
	dutil.Advertise(ctx, routingDiscovery, config.RendezvousString)

	dhtChan, err := routingDiscovery.FindPeers(ctx, config.RendezvousString)
	if err != nil {
		panic(err)
	}

	// Merge mDNS and DHT discovery
	go func() {
		for peer := range dhtChan {
			tryConnect(ctx, host, peer, config.ProtocolID)
		}
	}()
	go func() {
		for peer := range mdnsChan {
			tryConnect(ctx, host, peer, config.ProtocolID)
		}
	}()

	// After launching discovery routines, start reading console input.
	go readConsole()

	select {}
}

func handleStream(stream network.Stream) {
	fmt.Println("📡 Incoming stream from", stream.Conn().RemotePeer())
	fmt.Print("💬 ")
	rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))
	// Register the new stream.
	peersMu.Lock()
	peers = append(peers, rw)
	peersMu.Unlock()
	go readData(rw)
}

// tryConnect now only dials if our PeerID is “less” than theirs, avoiding simultaneous dials.
func tryConnect(ctx context.Context, h host.Host, pi peer.AddrInfo, pid string) {
	if pi.ID == h.ID() {
		return
	}

	// tie-breaker: only dial if our ID is lexicographically smaller
	if h.ID().String() >= pi.ID.String() {
		return
	}

	// add discovered addresses to the peerstore for 1 hour
	h.Peerstore().AddAddrs(pi.ID, pi.Addrs, time.Hour)

	fmt.Println("🔍 Dialing", pi.ID, "…")
	ctxC, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := h.Connect(ctxC, pi); err != nil {
		fmt.Println("🔗 Dial error for", pi.ID, ":", err)
		go retryConnect(ctx, h, pi, pid)
		return
	}

	// open our protocol stream
	stream, err := h.NewStream(ctx, pi.ID, protocol.ID(pid))
	if err != nil {
		fmt.Println("🔗 Stream open failed for", pi.ID, ":", err)
		go retryConnect(ctx, h, pi, pid)
		return
	}

	fmt.Println("🔗 Connected & stream open to", pi.ID)
	fmt.Print("💬 ")
	rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))

	peersMu.Lock()
	peers = append(peers, rw)
	peersMu.Unlock()

	go readData(rw)
}

// retryConnect waits and then re-attempts dialing (still honoring the tie-breaker).
func retryConnect(ctx context.Context, h host.Host, pi peer.AddrInfo, pid string) {
	time.Sleep(5 * time.Second)

	// if already connected, skip it
	for _, c := range h.Network().Conns() {
		if c.RemotePeer() == pi.ID {
			fmt.Println("✅ Already connected to", pi.ID)
			return
		}
	}

	fmt.Println("🔄 Retrying dial to", pi.ID)
	tryConnect(ctx, h, pi, pid)
}

// New function to remove a peer from the peers slice.
func removePeer(rw *bufio.ReadWriter) {
	peersMu.Lock()
	defer peersMu.Unlock()
	for i, peerRW := range peers {
		if peerRW == rw {
			// Remove the peer at index i
			peers = append(peers[:i], peers[i+1:]...)
			break
		}
	}
}

func readData(rw *bufio.ReadWriter) {
	for {
		msg, err := rw.ReadString('\n')
		if err != nil {
			removePeer(rw)
			return
		}
		msg = strings.TrimSpace(msg)
		if msg == "" {
			continue
		}
		parts := strings.SplitN(msg, ":", 3)
		switch parts[0] {
		case "FILE":
			if len(parts) != 3 {
				fmt.Println("❌ Invalid FILE message format")
				continue
			}
			filename := parts[1]
			rawData, err := base64.StdEncoding.DecodeString(parts[2])
			if err != nil {
				fmt.Printf("❌ Failed to decode file %s: %s\n", filename, err)
				continue
			}
			outputDir := "./output"
			os.MkdirAll(outputDir, os.ModePerm)
			outputPath := filepath.Join(outputDir, filename)
			if err := os.WriteFile(outputPath, rawData, 0644); err != nil {
				fmt.Printf("❌ Failed to save \"%s\": %s\n", filename, err)
			} else {
				fmt.Printf("📄 Received file \"%s\" → %s\n", filename, outputPath)
			}
		case "CODE":
			if len(parts) != 3 {
				fmt.Println("❌ Invalid CODE message format")
				continue
			}
			// parts[1] can be "filename|lang", e.g. "main.go|go"
			meta := strings.SplitN(parts[1], "|", 2)
			filename := meta[0]
			lang := "txt"
			if len(meta) == 2 {
				lang = meta[1]
			}
			rawCode, err := base64.StdEncoding.DecodeString(parts[2])
			if err != nil {
				fmt.Printf("❌ Failed to decode code %s: %s\n", filename, err)
				continue
			}
			outputDir := "./output"
			os.MkdirAll(outputDir, os.ModePerm)
			outputPath := filepath.Join(outputDir, filename)
			if err := os.WriteFile(outputPath, rawCode, 0644); err != nil {
				fmt.Printf("❌ Failed to save code \"%s\": %s\n", filename, err)
			} else {
				fmt.Printf("🖥️ Code \"%s\" (lang=%s) → %s\n", filename, lang, outputPath)
			}
		default:
			// For plain chat messages, extract the sender's PeerID.
			// expected format: "PeerID: message"
			splitMsg := strings.SplitN(msg, ": ", 2)
			sender, content := "unknown", msg
			if len(splitMsg) == 2 {
				sender = splitMsg[0]
				content = splitMsg[1]
			}
			fmt.Printf("\n\x1b[32m%s\x1b[0m ▷ %s\n", sender, content)
			fmt.Print("💬 ")
		}
	}
}

// New function to continuously read console input and broadcast messages.
func readConsole() {
	stdin := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("💬 ")
		input, err := stdin.ReadString('\n')
		if err != nil {
			return
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		var toSend string
		switch {
		case strings.HasPrefix(input, "/share "):
			filename := strings.TrimSpace(strings.TrimPrefix(input, "/share "))
			data, err := os.ReadFile(filename)
			if err != nil {
				fmt.Println("Error reading file:", err)
				continue
			}
			encoded := base64.StdEncoding.EncodeToString(data)
			toSend = fmt.Sprintf("FILE:%s:%s", filepath.Base(filename), encoded)

		case strings.HasPrefix(input, "/code "):
			// usage: /code <filename> [lang]
			parts := strings.Fields(input)
			if len(parts) < 2 {
				fmt.Println("Usage: /code <filename> [language]")
				continue
			}
			filename := parts[1]
			lang := "txt"
			if len(parts) >= 3 {
				lang = parts[2]
			}
			data, err := os.ReadFile(filename)
			if err != nil {
				fmt.Println("Error reading file:", err)
				continue
			}
			encoded := base64.StdEncoding.EncodeToString(data)
			// embed language after a pipe
			toSend = fmt.Sprintf("CODE:%s|%s:%s", filepath.Base(filename), lang, encoded)

		default:
			// Prefix plain chat messages with selfAlias.
			toSend = fmt.Sprintf("%s: %s", selfAlias, input)
		}

		broadcastMessage(toSend)
	}
}

// Function to broadcast a message to all connected peers.
func broadcastMessage(message string) {
	peersMu.Lock()
	defer peersMu.Unlock()
	for _, rw := range peers {
		if _, err := rw.WriteString(message + "\n"); err != nil {
			removePeer(rw)
			continue
		}
		rw.Flush()
	}
}

type addrList []maddr.Multiaddr

func (al *addrList) String() string {
	var out []string
	for _, addr := range *al {
		out = append(out, addr.String())
	}
	return strings.Join(out, ",")
}

func (al *addrList) Set(value string) error {
	addr, err := maddr.NewMultiaddr(value)
	if err != nil {
		return err
	}
	*al = append(*al, addr)
	return nil
}

type Config struct {
	RendezvousString string
	ListenAddresses  addrList
	ProtocolID       string
	Alias            string // new field for alias
}

func ParseFlags() (Config, error) {
	var config Config
	flag.StringVar(&config.RendezvousString, "rendezvous", "chatroom", "Unique string to identify peer group")
	flag.Var(&config.ListenAddresses, "listen", "Multiaddress to listen on")
	flag.StringVar(&config.ProtocolID, "pid", "/chat/1.1.0", "Protocol ID for streams")
	flag.StringVar(&config.Alias, "alias", "", "Alias for chat. If empty, uses PeerID")
	flag.Parse()

	if len(config.ListenAddresses) == 0 {
		addr, _ := maddr.NewMultiaddr("/ip4/0.0.0.0/tcp/0")
		config.ListenAddresses = append(config.ListenAddresses, addr)
	}

	return config, nil
}

type mdnsNotifee struct {
	PeerChan chan peer.AddrInfo
}

func (n *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	// Debug: log discovered peer via mDNS.
	fmt.Println("👀 Discovered via mdns:", pi.ID)
	n.PeerChan <- pi
}

func initMDNS(h host.Host, rendezvous string) chan peer.AddrInfo {
	peerChan := make(chan peer.AddrInfo)
	notifee := &mdnsNotifee{PeerChan: peerChan}
	service := mdns.NewMdnsService(h, rendezvous, notifee)
	if err := service.Start(); err != nil {
		panic(err)
	}
	return peerChan
}
