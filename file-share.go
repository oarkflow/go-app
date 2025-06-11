// main.go
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

	"github.com/grandcat/zeroconf"
	"github.com/jackpal/gateway"
	natpmp "github.com/jackpal/go-nat-pmp"
	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	peer "github.com/libp2p/go-libp2p/core/peer"
	peerstore "github.com/libp2p/go-libp2p/core/peerstore"
	multiaddr "github.com/multiformats/go-multiaddr"
	webrtc "github.com/pion/webrtc/v3"
)

const (
	mdnsService = "_p2pchat._udp"
	mdnsDomain  = "local."
	topicName   = "p2pchat-room"
	ttl         = 3600 // NAT-PMP lease time
)

type signalMsg struct {
	Type string `json:"type"` // "offer" or "answer"
	SDP  string `json:"sdp"`
}

var (
	udpConn net.PacketConn
	psTopic *pubsub.Topic
	once    sync.Once
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 1) Random UDP port for LAN signaling
	rand.Seed(time.Now().UnixNano())
	sigPort := rand.Intn(10000) + 20000

	// 2) NAT-PMP port mapping
	if gwIP, err := gateway.DiscoverGateway(); err == nil {
		client := natpmp.NewClient(gwIP)
		if _, err := client.AddPortMapping("udp", sigPort, sigPort, ttl); err == nil {
			log.Printf("→ NAT-PMP mapped UDP port %d\n", sigPort)
		} else {
			log.Println("! NAT-PMP port mapping failed:", err)
		}
	} else {
		log.Println("! No NAT gateway found:", err)
	}

	// 3) Listen for LAN signaling (UDP)
	var err error
	udpConn, err = net.ListenPacket("udp4", fmt.Sprintf(":%d", sigPort))
	if err != nil {
		log.Fatal("UDP listen:", err)
	}
	defer udpConn.Close()

	// 4) Create a libp2p host over TCP
	host, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
	if err != nil {
		log.Fatal("libp2p New:", err)
	}

	// 5) Kademlia DHT + bootstrap (convert each Multiaddr → AddrInfo)
	kad, err := dht.New(ctx, host)
	if err != nil {
		log.Fatal(err)
	}
	if err := kad.Bootstrap(ctx); err != nil {
		log.Fatal(err)
	}
	for _, maddr := range dht.DefaultBootstrapPeers {
		ai, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			continue
		}
		host.Peerstore().AddAddrs(ai.ID, ai.Addrs, peerstore.PermanentAddrTTL)
		if err := host.Connect(ctx, *ai); err != nil {
			log.Println("bootstrap connect:", err)
		}
	}

	// 6) PubSub for Internet-wide signaling
	ps, err := pubsub.NewGossipSub(ctx, host)
	if err != nil {
		log.Fatal(err)
	}
	psTopic, err = ps.Join(topicName)
	if err != nil {
		log.Fatal(err)
	}
	sub, err := psTopic.Subscribe()
	if err != nil {
		log.Fatal(err)
	}

	// 7) mDNS announce our node + info
	nodeID := fmt.Sprintf("node-%04d", rand.Intn(10000))
	txt := []string{fmt.Sprintf("port=%d", sigPort)}
	for _, ma := range host.Addrs() {
		txt = append(txt, ma.String())
	}
	mdnsServer, err := zeroconf.Register(nodeID, mdnsService, mdnsDomain, sigPort, txt, nil)
	if err != nil {
		log.Fatal("mDNS register:", err)
	}
	defer mdnsServer.Shutdown()

	// 8) Browse mDNS in background
	resolver, _ := zeroconf.NewResolver(nil)
	entries := make(chan *zeroconf.ServiceEntry)
	go func() {
		if err := resolver.Browse(ctx, mdnsService, mdnsDomain, entries); err != nil {
			log.Println("mDNS browse:", err)
		}
	}()

	// 9) Prepare WebRTC
	m := webrtc.SettingEngine{}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(m))
	cfg := webrtc.Configuration{} // no ICE servers

	negotiate := func(isOfferer bool) {
		once.Do(func() {
			pc, err := api.NewPeerConnection(cfg)
			if err != nil {
				log.Fatal(err)
			}
			dc, err := pc.CreateDataChannel("data", nil)
			if err != nil {
				log.Fatal(err)
			}
			dc.OnOpen(func() {
				log.Println("DataChannel opened! Type and press enter:")
				go func() {
					scan := bufio.NewScanner(os.Stdin)
					for scan.Scan() {
						dc.SendText(scan.Text())
					}
				}()
			})
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				fmt.Printf("← %s\n", string(msg.Data))
			})

			if isOfferer {
				offer, _ := pc.CreateOffer(nil)
				pc.SetLocalDescription(offer)
				broadcast(signalMsg{"offer", offer.SDP})
			}
			go udpListener(pc)
			go pubsubListener(pc, sub)
		})
	}

	// 10) Discovery loops
	go func() {
		for e := range entries {
			if e.Instance == nodeID {
				continue
			}
			log.Println("→ LAN peer:", e.Instance)
			for _, txt := range e.Text {
				if !strings.HasPrefix(txt, "/") {
					continue
				}
				ma, err := multiaddr.NewMultiaddr(txt)
				if err != nil {
					continue
				}
				ai, err := peer.AddrInfoFromP2pAddr(ma)
				if err != nil {
					continue
				}
				host.Peerstore().AddAddrs(ai.ID, ai.Addrs, peerstore.PermanentAddrTTL)
				if err := host.Connect(ctx, *ai); err != nil {
					log.Println("libp2p connect:", err)
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
			var m signalMsg
			if json.Unmarshal(ev.Data, &m) == nil {
				log.Println("→ PubSub signal:", m.Type)
				negotiate(m.Type == "offer")
			}
		}
	}()

	log.Printf("Running as %s – Ctrl+C to quit\n", nodeID)
	<-ctx.Done()
}

// broadcast sends SDP over LAN (multicast UDP) and the PubSub topic.
func broadcast(sig signalMsg) {
	b, _ := json.Marshal(sig)
	udpConn.WriteTo(b, &net.UDPAddr{IP: net.ParseIP("224.0.0.251"), Port: 5353})
	psTopic.Publish(context.Background(), b)
}

func udpListener(pc *webrtc.PeerConnection) {
	buf := make([]byte, 1600)
	for {
		n, _, _ := udpConn.ReadFrom(buf)
		var m signalMsg
		if json.Unmarshal(buf[:n], &m) == nil {
			apply(pc, m)
		}
	}
}

func pubsubListener(pc *webrtc.PeerConnection, sub *pubsub.Subscription) {
	for {
		ev, err := sub.Next(context.Background())
		if err != nil {
			return
		}
		var m signalMsg
		if json.Unmarshal(ev.Data, &m) == nil {
			apply(pc, m)
		}
	}
}

func apply(pc *webrtc.PeerConnection, m signalMsg) {
	switch m.Type {
	case "offer":
		pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP})
		ans, _ := pc.CreateAnswer(nil)
		pc.SetLocalDescription(ans)
		broadcast(signalMsg{"answer", ans.SDP})
	case "answer":
		pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: m.SDP})
	}
}
