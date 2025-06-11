package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackpal/gateway"

	"github.com/grandcat/zeroconf"
	natpmp "github.com/jackpal/go-nat-pmp"
	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	peer "github.com/libp2p/go-libp2p/core/peer" // added for AddrInfoFromP2pAddr
	peerstore "github.com/libp2p/go-libp2p/core/peerstore"
	multiaddr "github.com/multiformats/go-multiaddr"
	webrtc "github.com/pion/webrtc/v3"
)

const (
	ServiceType  = "_p2pchat._udp"
	Domain       = "local."
	PubSubTopic  = "p2pchat-room"
	sigPortConst = 5353 // used for signaling over multicast
)

type Signal struct {
	Type string `json:"type"` // "offer" or "answer"
	SDP  string `json:"sdp"`
}

// Global variables for signaling
var sigUdpConn net.PacketConn
var sigTopic *pubsub.Topic

// Helper to return a port number for signaling.
func sigPort(b []byte) int {
	return sigPortConst
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Random UDP port for LAN signaling
	rand.Seed(time.Now().UnixNano())
	udpPort := rand.Intn(10000) + 10000

	gatewayIP, err := gateway.DiscoverGateway()
	if err != nil {
		return
	}

	gateway := natpmp.NewClient(gatewayIP)
	if gateway != nil {
		if _, err := gateway.AddPortMapping("udp", udpPort, udpPort, 3600); err == nil {
			log.Println("Mapped UDP port via NAT-PMP:", udpPort)
		}
	}

	// 2) LAN signaling listener
	udpConn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", udpPort))
	if err != nil {
		log.Fatal("UDP listen failed:", err)
	}
	sigUdpConn = udpConn // assign global variable
	defer udpConn.Close()

	// 3) libp2p host + DHT + pubsub
	host, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"), // corrected multiaddr
	)
	if err != nil {
		log.Fatal(err)
	}
	kademliaDHT, err := dht.New(ctx, host)
	if err != nil {
		log.Fatal(err)
	}
	if err := kademliaDHT.Bootstrap(ctx); err != nil {
		log.Fatal(err)
	}
	ps, err := pubsub.NewGossipSub(ctx, host)
	if err != nil {
		log.Fatal(err)
	}
	topic, _ := ps.Join(PubSubTopic)
	sigTopic = topic // assign global variable
	sub, _ := topic.Subscribe()

	// Gather host multiaddrs
	addrs := host.Addrs()
	addrStrings := make([]string, len(addrs))
	for i, addr := range addrs {
		addrStrings[i] = addr.String()
	}

	// 4) mDNS announce our nodeID, UDP port, and libp2p addrs via TXT
	nodeID := fmt.Sprintf("node-%04d", rand.Intn(10000))
	txt := append([]string{fmt.Sprintf("port=%d", udpPort)}, addrStrings...)
	mdnsSrv, err := zeroconf.Register(nodeID, ServiceType, Domain, udpPort, txt, nil)
	if err != nil {
		log.Fatalln("mDNS register error:", err)
	}
	defer mdnsSrv.Shutdown()

	// mDNS browse
	resolver, _ := zeroconf.NewResolver(nil)
	entries := make(chan *zeroconf.ServiceEntry)
	go func() {
		if err := resolver.Browse(ctx, ServiceType, Domain, entries); err != nil {
			log.Println("mDNS browse error:", err)
		}
	}()

	// 5) WebRTC API setup
	m := webrtc.SettingEngine{}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(m))
	config := webrtc.Configuration{}

	var once sync.Once
	negotiate := func(isOfferer bool) {
		once.Do(func() {
			pc, err := api.NewPeerConnection(config)
			if err != nil {
				log.Fatal(err)
			}
			dc, err := pc.CreateDataChannel("data", nil)
			if err != nil {
				log.Fatal(err)
			}
			dc.OnOpen(func() {
				log.Println("Data channel opened")
				go func() {
					scanner := bufio.NewScanner(os.Stdin)
					for scanner.Scan() {
						dc.SendText(scanner.Text())
					}
				}()
			})
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				log.Printf("Received: %s", string(msg.Data))
			})

			// Exchange offers/answers
			if isOfferer {
				offer, err := pc.CreateOffer(nil)
				if err != nil {
					log.Fatal(err)
				}
				pc.SetLocalDescription(offer)
				signalAll(udpConn, topic, Signal{"offer", offer.SDP})
			}

			// Handle incoming signaling
			go handleUDP(udpConn, pc)
			go handlePubSub(sub, pc)
		})
	}

	// Discovery: on LAN or DHT/pubsub
	go func() {
		for e := range entries {
			if e.Instance == nodeID {
				continue
			}
			log.Println("LAN peer found:", e.Instance)
			// connect to libp2p addrs from TXT
			for _, v := range e.Text {
				if strings.HasPrefix(v, "/") {
					if maddr, err := multiaddr.NewMultiaddr(v); err == nil {
						if pi, err := peer.AddrInfoFromP2pAddr(maddr); err == nil { // modified call
							host.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstore.PermanentAddrTTL)
							host.Connect(ctx, *pi)
						}
					}
				}
			}
			negotiate(nodeID > e.Instance)
		}
	}()
	go func() {
		for {
			ev, err := sub.Next(ctx)
			if err != nil {
				return
			}
			var s Signal
			if json.Unmarshal(ev.Data, &s) == nil && s.Type != "" {
				log.Println("PubSub signaling event")
				negotiate(s.Type == "answer")
			}
		}
	}()

	log.Println("Node ID:", nodeID)
	<-ctx.Done()
}

// signalAll broadcasts the Signal via UDP and PubSub.
func signalAll(udpConn net.PacketConn, topic *pubsub.Topic, sig Signal) {
	b, _ := json.Marshal(sig)
	udpConn.WriteTo(b, &net.UDPAddr{IP: net.ParseIP("224.0.0.251"), Port: sigPort(b)})
	topic.Publish(context.Background(), b)
}

func handleUDP(conn net.PacketConn, pc *webrtc.PeerConnection) {
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			continue
		}
		var s Signal
		if json.Unmarshal(buf[:n], &s) != nil {
			continue
		}
		applySignal(pc, s)
	}
}

func handlePubSub(sub *pubsub.Subscription, pc *webrtc.PeerConnection) {
	for {
		ev, err := sub.Next(context.Background())
		if err != nil {
			return
		}
		var s Signal
		if json.Unmarshal(ev.Data, &s) == nil {
			applySignal(pc, s)
		}
	}
}

// In applySignal, replace the pc.WriteRTCP call with a broadcast using signalAll.
func applySignal(pc *webrtc.PeerConnection, s Signal) {
	switch s.Type {
	case "offer":
		pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: s.SDP})
		ans, _ := pc.CreateAnswer(nil)
		pc.SetLocalDescription(ans)
		// Instead of pc.WriteRTCP(b), use signalAll with global variables.
		signalAll(sigUdpConn, sigTopic, Signal{"answer", ans.SDP})
	case "answer":
		pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: s.SDP})
	}
}
