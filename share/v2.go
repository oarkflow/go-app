package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/routing"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	maddr "github.com/multiformats/go-multiaddr"
)

type PermissiveConnectionGater struct{}

func (cg *PermissiveConnectionGater) InterceptAddrDial(_ peer.ID, _ maddr.Multiaddr) bool {
	return true
}
func (cg *PermissiveConnectionGater) InterceptPeerDial(_ peer.ID) bool              { return true }
func (cg *PermissiveConnectionGater) InterceptAccept(n network.ConnMultiaddrs) bool { return true }
func (cg *PermissiveConnectionGater) InterceptSecured(_ network.Direction, _ peer.ID, _ network.ConnMultiaddrs) bool {
	return true
}
func (cg *PermissiveConnectionGater) InterceptUpgraded(_ network.Conn) (bool, control.DisconnectReason) {
	return true, 0
}

var logger = log.Logger("rendezvous")

func handleStream(stream network.Stream) {
	logger.Info("Got a new stream!")
	defer stream.Close() // <-- Added to ensure stream is closed

	// Create a buffer stream for non-blocking read and write.
	rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))

	go readData(rw)
	go writeData(rw)

	// 'stream' will stay open until you close it (or the other side closes it).
}

func readData(rw *bufio.ReadWriter) {
	for {
		str, err := rw.ReadString('\n')
		if err != nil {
			logger.Error("Error reading from buffer", err)
			return // <-- Return instead of panic
		}

		if str == "" {
			return
		}
		if str != "\n" {
			// Green console colour: 	\x1b[32m
			// Reset console colour: 	\x1b[0m
			fmt.Printf("\x1b[32m%s\x1b[0m> ", str)
		}

	}
}

func writeData(rw *bufio.ReadWriter) {
	stdReader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("> ")
		sendData, err := stdReader.ReadString('\n')
		if err != nil {
			logger.Error("Error reading from stdin", err)
			return // <-- Return instead of panic
		}

		_, err = rw.WriteString(fmt.Sprintf("%s\n", sendData))
		if err != nil {
			logger.Error("Error writing to buffer", err)
			return // <-- Return instead of panic
		}
		err = rw.Flush()
		if err != nil {
			logger.Error("Error flushing buffer", err)
			return // <-- Return instead of panic
		}
	}
}

func main() {
	log.SetAllLoggers(log.LevelWarn)
	log.SetLogLevel("rendezvous", "info")
	help := flag.Bool("h", false, "Display Help")
	config, err := ParseFlags()
	if err != nil {
		panic(err)
	}

	if *help {
		fmt.Println("This program demonstrates a simple p2p chat application using libp2p")
		fmt.Println()
		fmt.Println("Usage: Run './chat in two different terminals. Let them connect to the bootstrap nodes, announce themselves and connect to the peers")
		flag.PrintDefaults()
		return
	}

	// Generate a persistent identity key to produce a valid TLS certificate.
	prvKey, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		panic(err)
	}

	ctx := context.Background()

	// Pre-parse bootstrap peers
	bootstrapPeers := make([]peer.AddrInfo, len(config.BootstrapPeers))
	for i, addr := range config.BootstrapPeers {
		pi, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			logger.Error("Error parsing bootstrap address", err)
			continue
		}
		bootstrapPeers[i] = *pi
	}

	// Build host options based on provided config:
	opts := []libp2p.Option{
		libp2p.Identity(prvKey),
		libp2p.ListenAddrs([]maddr.Multiaddr(config.ListenAddresses)...),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
		libp2p.ConnectionGater(&PermissiveConnectionGater{}),
		// Configure DHT as routing implementation
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			dhtInstance, err := dht.New(
				ctx, h,
				dht.Mode(dht.ModeAutoServer), // Auto switch between client/server mode
				dht.BootstrapPeers(bootstrapPeers...),
			)
			return dhtInstance, err
		}),
	}
	// Enable AutoRelay regardless of static relays
	if len(config.StaticRelays) > 0 {
		opts = append(opts, libp2p.EnableRelay())
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(parseRelays(config.StaticRelays)))
	} else {
		// Only enable AutoRelay if we have functional DHT
		opts = append(opts, libp2p.EnableRelay())
	}
	host, err := libp2p.New(opts...)
	if err != nil {
		panic(err)
	}
	logger.Info("Host created. We are:", host.ID())
	logger.Info(host.Addrs())

	// Set a function as stream handler. This function is called when a peer
	// initiates a connection and starts a stream with this peer.
	host.SetStreamHandler(protocol.ID(config.ProtocolID), handleStream)
	kademliaDHT, err := dht.New(ctx, host, dht.Mode(dht.ModeServer), dht.BootstrapPeers(bootstrapPeers...))
	if err != nil {
		panic(err)
	}

	// Bootstrap the DHT. In the default configuration, this spawns a Background
	// thread that will refresh the peer table every five minutes.
	logger.Debug("Bootstrapping the DHT")
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		panic(err)
	}
	// Wait for bootstrap to complete
	logger.Info("Waiting for DHT bootstrap...")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	timeout := time.After(30 * time.Second)
BootstrapWait:
	for {
		select {
		case <-ticker.C:
			if len(kademliaDHT.RoutingTable().ListPeers()) > 0 {
				logger.Info("DHT bootstrap completed with peers")
				break BootstrapWait
			}
		case <-timeout:
			logger.Warn("DHT bootstrap timeout, continuing with existing peers")
			break BootstrapWait
		}
	}

	// Start continuous advertising and peer discovery
	routingDiscovery := drouting.NewRoutingDiscovery(kademliaDHT)

	// Periodic advertising
	go func() {
		for {
			dutil.Advertise(ctx, routingDiscovery, config.RendezvousString)
			logger.Info("Advertising ourselves...")
			time.Sleep(30 * time.Second)
		}
	}()

	// Continuous peer discovery
	go func() {
		for {
			logger.Info("Searching for peers...")
			peerChan, err := routingDiscovery.FindPeers(ctx, config.RendezvousString)
			if err != nil {
				logger.Error("Peer discovery failed", err)
				time.Sleep(5 * time.Second)
				continue
			}

			for peer := range peerChan {
				if peer.ID == host.ID() || len(peer.Addrs) == 0 {
					continue
				}
				logger.Debugf("Found peer %s", peer.ID)

				// Attempt connection
				logger.Debugf("Connecting to %s", peer.ID)
				if err := host.Connect(ctx, peer); err != nil {
					logger.Warnf("Failed to connect to %s: %v", peer.ID, err)
					continue
				}

				// Open stream after successful connection
				stream, err := host.NewStream(ctx, peer.ID, protocol.ID(config.ProtocolID))
				if err != nil {
					logger.Warnf("Failed to open stream to %s: %v", peer.ID, err)
					continue
				}

				logger.Infof("Connected to %s", peer.ID)
				go handleStream(stream) // Use the same stream handler
			}
			time.Sleep(30 * time.Second)
		}
	}()

	logger.Info("Chat client ready")
	select {} // Block forever
}

func parseRelays(addrs []maddr.Multiaddr) []peer.AddrInfo {
	var relays []peer.AddrInfo
	for _, addr := range addrs {
		pi, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			logger.Error("Error parsing relay address", err)
			continue
		}
		relays = append(relays, *pi)
	}
	return relays
}

// A new type we need for writing a custom flag parser
type addrList []maddr.Multiaddr

func (al *addrList) String() string {
	strs := make([]string, len(*al))
	for i, addr := range *al {
		strs[i] = addr.String()
	}
	return strings.Join(strs, ",")
}

func (al *addrList) Set(value string) error {
	addr, err := maddr.NewMultiaddr(value)
	if err != nil {
		return err
	}
	*al = append(*al, addr)
	return nil
}

func StringsToAddrs(addrStrings []string) (maddrs []maddr.Multiaddr, err error) {
	for _, addrString := range addrStrings {
		addr, err := maddr.NewMultiaddr(addrString)
		if err != nil {
			return maddrs, err
		}
		maddrs = append(maddrs, addr)
	}
	return
}

type Config struct {
	RendezvousString string
	BootstrapPeers   addrList
	ListenAddresses  addrList
	ProtocolID       string
	StaticRelays     addrList // <-- New field for static relay addresses
}

func ParseFlags() (Config, error) {
	config := Config{}
	flag.StringVar(&config.RendezvousString, "rendezvous", "oarkflow34616",
		"Unique string to identify group of nodes. Share this with your friends to let them connect with you")
	flag.Var(&config.BootstrapPeers, "peer", "Adds a peer multiaddress to the bootstrap list")
	flag.Var(&config.ListenAddresses, "listen", "Adds a multiaddress to the listen list")
	flag.Var(&config.StaticRelays, "relay", "Adds a relay multiaddress to static relays list for auto relay") // <-- New flag for static relays
	flag.StringVar(&config.ProtocolID, "pid", "/chat/1.1.0", "Sets a protocol id for stream headers")
	flag.Parse()

	if len(config.BootstrapPeers) == 0 {
		config.BootstrapPeers = dht.DefaultBootstrapPeers
	}

	return config, nil
}
