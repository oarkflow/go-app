package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
)

const (
	ProtocolName    = "/p2p-chat/1.0.0"
	DiscoveryPrefix = "p2p-chat-discovery"
)

var (
	peers     = make(map[peer.ID]*bufio.ReadWriter)
	peersLock sync.Mutex
	localPID  peer.ID
)

func main() {
	ctx := context.Background()

	// Configuration
	rendezvous := flag.String("rendezvous", "default-chat-room", "Rendezvous point for peer discovery")
	listenPort := flag.Int("port", 0, "Port to listen on")
	flag.Parse()

	// Create libp2p host
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", *listenPort),
			fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", *listenPort),
		),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
	)
	if err != nil {
		log.Fatal("Failed to create host:", err)
	}
	defer h.Close()
	localPID = h.ID()

	// Create and initialize DHT
	dhtInst, err := dht.New(ctx, h, dht.Mode(dht.ModeAutoServer))
	if err != nil {
		log.Fatal("DHT creation failed:", err)
	}

	// Bootstrap DHT
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := dhtInst.Bootstrap(ctx); err != nil {
				log.Printf("DHT bootstrap error: %v", err)
			}
		}
	}()

	// Setup peer discovery
	routingDiscovery := drouting.NewRoutingDiscovery(dhtInst)
	dutil.Advertise(ctx, routingDiscovery, *rendezvous)

	// Set stream handler
	h.SetStreamHandler(protocol.ID(ProtocolName), handleStream)

	// Start peer discovery loop
	go discoverPeers(ctx, h, routingDiscovery, *rendezvous)

	log.Printf("Your Peer ID: %s", shortPID(h.ID()))
	log.Printf("Listening on: %v", h.Addrs())

	// Start chat interface
	startChat(h)
}

func discoverPeers(ctx context.Context, h host.Host, disc *drouting.RoutingDiscovery, rendezvous string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dutil.Advertise(ctx, disc, rendezvous)

			peerChan, err := disc.FindPeers(ctx, rendezvous)
			if err != nil {
				log.Printf("Peer discovery failed: %v", err)
				continue
			}

			for p := range peerChan {
				if p.ID == h.ID() || len(p.Addrs) == 0 {
					continue
				}

				ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()

				if err := h.Connect(ctx, p); err != nil {
					continue
				}
			}

		case <-ctx.Done():
			return
		}
	}
}

func handleStream(stream network.Stream) {
	defer stream.Close()
	remotePeer := stream.Conn().RemotePeer()

	rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))

	// Send our ID to the peer
	_, _ = rw.WriteString(fmt.Sprintf("SYSTEM:CONNECT:%s\n", localPID))
	_ = rw.Flush()

	peersLock.Lock()
	peers[remotePeer] = rw
	peersLock.Unlock()

	fmt.Printf("\x1b[34m[System] %s connected\x1b[0m\n", shortPID(remotePeer))

	go readLoop(remotePeer, rw)
}

func readLoop(peerID peer.ID, rw *bufio.ReadWriter) {
	for {
		msg, err := rw.ReadString('\n')
		if err != nil {
			break
		}

		msg = strings.TrimSpace(msg)
		if strings.HasPrefix(msg, "SYSTEM:CONNECT:") {
			// Handle system connection message
			parts := strings.Split(msg, ":")
			if len(parts) >= 3 {
				fmt.Printf("\x1b[34m[System] Connected to %s\x1b[0m\n", shortPIDFromMsg(parts[2]))
			}
			continue
		}

		if len(msg) > 0 {
			fmt.Printf("\x1b[32m%s\x1b[0m: %s\n", shortPID(peerID), msg)
		}
	}

	peersLock.Lock()
	delete(peers, peerID)
	peersLock.Unlock()
	fmt.Printf("\x1b[31m[System] %s disconnected\x1b[0m\n", shortPID(peerID))
}

func startChat(h host.Host) {
	fmt.Println("\nType messages and press enter to send:")
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}

		msg := scanner.Text()
		if msg == "" {
			continue
		}

		fullMsg := fmt.Sprintf("%s: %s", shortPID(h.ID()), msg)

		peersLock.Lock()
		for pid, rw := range peers {
			_, err := rw.WriteString(fullMsg + "\n")
			if err != nil {
				delete(peers, pid)
				continue
			}
			err = rw.Flush()
			if err != nil {
				delete(peers, pid)
			}
		}
		peersLock.Unlock()
	}
}

func shortPIDFromMsg(str string) string {
	if len(str) > 8 {
		return str[:8]
	}
	return str
}

func shortPID(p peer.ID) string {
	str := p.String()
	if len(str) > 8 {
		return str[:8]
	}
	return str
}
