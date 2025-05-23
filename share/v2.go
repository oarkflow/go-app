package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

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

func main() {
	logging.SetLogLevel("*", "warn")
	logging.SetLogLevel("chat", "info")

	config, err := ParseFlags()
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	host, err := libp2p.New(
		libp2p.ListenAddrs(config.ListenAddresses...),
		libp2p.Security(noise.ID, noise.New), // Use Noise security
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

	select {}
}

func handleStream(stream network.Stream) {
	fmt.Println("📡 Incoming stream from", stream.Conn().RemotePeer())
	rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))
	go writeData(rw)
	go readData(rw)
}

func tryConnect(ctx context.Context, h host.Host, peerInfo peer.AddrInfo, pid string) {
	if peerInfo.ID == h.ID() {
		return
	}
	if err := h.Connect(ctx, peerInfo); err != nil {
		logger.Warn("❌ Failed to connect to", peerInfo.ID, ":", err)
		return
	}
	fmt.Println("🔗 Connected to", peerInfo.ID)

	stream, err := h.NewStream(ctx, peerInfo.ID, protocol.ID(pid))
	if err != nil {
		logger.Warn("❌ Failed to open stream:", err)
		return
	}

	rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))
	go writeData(rw)
	go readData(rw)
}

func readData(rw *bufio.ReadWriter) {
	for {
		msg, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		if msg = strings.TrimSpace(msg); msg != "" {
			fmt.Printf("\x1b[32m%s\x1b[0m > ", msg)
		}
	}
}

func writeData(rw *bufio.ReadWriter) {
	stdin := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		msg, err := stdin.ReadString('\n')
		if err != nil {
			return
		}
		_, err = rw.WriteString(msg)
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
