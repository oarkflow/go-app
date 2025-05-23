package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

	logging "github.com/ipfs/go-log/v2"
	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
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
	host, err := libp2p.New(
		libp2p.ListenAddrs(config.ListenAddresses...),
		libp2p.Security(noise.ID, noise.New),
	)
	if err != nil {
		panic(err)
	}

	host.SetStreamHandler(protocol.ID(config.ProtocolID), handleStream)

	fmt.Printf("Your Peer ID: %s\n", host.ID())
	for _, addr := range host.Addrs() {
		fmt.Printf(" - %s/p2p/%s\n", addr, host.ID().ShortString())
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
	rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))
	// Register the new stream.
	peersMu.Lock()
	peers = append(peers, rw)
	peersMu.Unlock()
	go readData(rw)
}

func tryConnect(ctx context.Context, h host.Host, peerInfo peer.AddrInfo, pid string) {
	if peerInfo.ID == h.ID() {
		return
	}
	if err := h.Connect(ctx, peerInfo); err != nil {
		if strings.Contains(err.Error(), "noise: message is too short") {
			// Quietly ignore noise handshake errors.
			return
		}
		// Minimal log for other errors.
		fmt.Println("🔗 Connection skipped for", peerInfo.ID)
		return
	}
	fmt.Println("🔗 Connected to", peerInfo.ID)

	stream, err := h.NewStream(ctx, peerInfo.ID, protocol.ID(pid))
	if err != nil {
		// Minimal error output.
		fmt.Println("🔗 Stream open failed:", err)
		return
	}
	rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))
	// Register the new stream.
	peersMu.Lock()
	peers = append(peers, rw)
	peersMu.Unlock()
	go readData(rw)
}

func readData(rw *bufio.ReadWriter) {
	for {
		msg, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		msg = strings.TrimSpace(msg)
		if msg == "" {
			continue
		}
		if strings.HasPrefix(msg, "FILE:") {
			parts := strings.SplitN(msg, "\n", 2)
			filename := strings.TrimPrefix(parts[0], "FILE:")
			fileContent := ""
			if len(parts) > 1 {
				fileContent = parts[1]
			}
			fmt.Printf("\n📄 Received file \"%s\":\n%s\n", filename, fileContent)
		} else if strings.HasPrefix(msg, "CODE:") {
			parts := strings.SplitN(msg, "\n", 3)
			filename := strings.TrimPrefix(parts[0], "CODE:")
			language := "go"
			codeContent := ""
			if len(parts) >= 3 {
				if parts[1] != "" {
					language = parts[1]
				}
				codeContent = parts[2]
			}
			fmt.Printf("\n🖥️ Code received for \"%s\" [%s]:\n%s\n", filename, language, codeContent)
		} else {
			// Print regular chat message with a colored prompt.
			fmt.Printf("\n\x1b[32m%s\x1b[0m ▷ ", msg)
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
		// Process commands for file or code sharing.
		if strings.HasPrefix(input, "/share ") {
			parts := strings.SplitN(input, " ", 2)
			if len(parts) < 2 {
				fmt.Println("Usage: /share <filename>")
				continue
			}
			filename := parts[1]
			data, err := os.ReadFile(filename)
			if err != nil {
				fmt.Println("Error reading file:", err)
				continue
			}
			input = fmt.Sprintf("FILE:%s\n%s", filename, string(data))
		} else if strings.HasPrefix(input, "/code ") {
			parts := strings.SplitN(input, " ", 3)
			if len(parts) < 2 {
				fmt.Println("Usage: /code <filename> [language]")
				continue
			}
			filename := parts[1]
			language := "go"
			if len(parts) == 3 && parts[2] != "" {
				language = parts[2]
			}
			data, err := os.ReadFile(filename)
			if err != nil {
				fmt.Println("Error reading file:", err)
				continue
			}
			input = fmt.Sprintf("CODE:%s\n%s\n%s", filename, language, string(data))
		}
		broadcastMessage(input)
	}
}

// Function to broadcast a message to all connected peers.
func broadcastMessage(message string) {
	peersMu.Lock()
	defer peersMu.Unlock()
	for _, rw := range peers {
		_, err := rw.WriteString(message + "\n")
		if err == nil {
			rw.Flush()
		}
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
}

func ParseFlags() (Config, error) {
	var config Config
	flag.StringVar(&config.RendezvousString, "rendezvous", "chatroom", "Unique string to identify peer group")
	flag.Var(&config.ListenAddresses, "listen", "Multiaddress to listen on")
	flag.StringVar(&config.ProtocolID, "pid", "/chat/1.1.0", "Protocol ID for streams")
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
