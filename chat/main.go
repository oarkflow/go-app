// main.go
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	
	"github.com/pion/webrtc/v3"
)

// -------------------------------
// Common types and utilities
// -------------------------------

// SignalingMessage is used to exchange SDP between host and peer.
type SignalingMessage struct {
	SDP string `json:"sdp"`
}

// generateASCIIQRCode is a minimal simulation of a QR code in ASCII that embeds text.
func generateASCIIQRCode(text string) string {
	border := strings.Repeat("=", len(text)+4)
	var buf bytes.Buffer
	buf.WriteString(border + "\n")
	buf.WriteString("| " + text + " |\n")
	buf.WriteString(border + "\n")
	buf.WriteString("(Copy the above code if your device cannot scan it.)")
	return buf.String()
}

// getOutboundIP attempts to obtain the machine’s non-loopback IP.
func getOutboundIP() (net.IP, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP, nil
}

// -------------------------------
// Global chat data channel wrapper
// -------------------------------

// ChatConn wraps a WebRTC data channel and a mutex.
type ChatConn struct {
	DC *webrtc.DataChannel
	mu sync.Mutex
	// messages is a callback to be called when a message arrives.
	OnMessage func(msg string)
}

func (c *ChatConn) SendMessage(msg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.DC.SendText(msg)
}

// -------------------------------
// Host Implementation
// -------------------------------

// hostState holds the host’s state.
type hostState struct {
	pc         *webrtc.PeerConnection
	chatConn   *ChatConn
	answerGot  chan struct{}
	clientsMux sync.Mutex
	// (For multi-peer scenarios, you could store multiple chat channels.)
}

func runHost(port int) error {
	// Create a new PeerConnection configuration.
	// No external ICE servers: we rely on direct connection.
	webrtcSetting := webrtc.SettingEngine{}
	// In a real NAT environment, you might want to use STUN servers,
	// but here we want pure peer-to-peer without external services.
	api := webrtc.NewAPI(webrtc.WithSettingEngine(webrtcSetting))
	config := webrtc.Configuration{}
	
	// Create the PeerConnection.
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("Error creating PeerConnection: %v", err)
	}
	
	// Create a DataChannel for chat and (optionally) file sharing.
	dataChannel, err := pc.CreateDataChannel("chat", nil)
	if err != nil {
		return fmt.Errorf("Error creating DataChannel: %v", err)
	}
	host := &hostState{
		pc:        pc,
		answerGot: make(chan struct{}),
	}
	
	chatConn := &ChatConn{
		DC: dataChannel,
		OnMessage: func(msg string) {
			log.Printf("Received: %s", msg)
		},
	}
	host.chatConn = chatConn
	
	// Set handlers for DataChannel events.
	dataChannel.OnOpen(func() {
		log.Println("DataChannel 'chat' open!")
	})
	dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
		// Simply log incoming messages.
		chatConn.OnMessage(string(msg.Data))
	})
	
	// Create an SDP offer.
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("Error creating offer: %v", err)
	}
	err = pc.SetLocalDescription(offer)
	if err != nil {
		return fmt.Errorf("Error setting local description: %v", err)
	}
	
	// Wait for ICE gathering to complete.
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	<-gatherComplete
	
	offerSDP := pc.LocalDescription().SDP
	// Encode the SDP offer in base64 to safely embed into QR.
	offerB64 := base64.StdEncoding.EncodeToString([]byte(offerSDP))
	
	// Get host’s outbound IP for constructing a signaling URL.
	ip, err := getOutboundIP()
	if err != nil {
		ip = net.ParseIP("127.0.0.1")
	}
	hostURL := "http://" + ip.String() + ":" + strconv.Itoa(port)
	
	// Construct signaling info: include offer and signaling URL.
	signalingInfo := offerB64 + "||" + hostURL + "/answer"
	
	// Start HTTP server for temporary signaling and UI.
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		tmpl := template.Must(template.New("host").Parse(hostHTML))
		data := struct {
			SignalingInfo string
			QR            string
		}{
			SignalingInfo: signalingInfo,
			QR:            generateASCIIQRCode(signalingInfo),
		}
		tmpl.Execute(w, data)
	})
	
	// /answer endpoint receives POST from a peer with its answer SDP.
	http.HandleFunc("/answer", func(w http.ResponseWriter, r *http.Request) {
		var msg SignalingMessage
		err := json.NewDecoder(r.Body).Decode(&msg)
		if err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		// Decode the answer SDP from base64.
		answerBytes, err := base64.StdEncoding.DecodeString(msg.SDP)
		if err != nil {
			http.Error(w, "Invalid SDP encoding", http.StatusBadRequest)
			return
		}
		answer := webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  string(answerBytes),
		}
		// Set remote description.
		err = pc.SetRemoteDescription(answer)
		if err != nil {
			http.Error(w, "Error setting remote description", http.StatusInternalServerError)
			return
		}
		log.Println("Remote description (answer) set successfully!")
		// Signal that we have an answer.
		close(host.answerGot)
		w.WriteHeader(http.StatusOK)
	})
	
	// An endpoint for sending chat messages from the UI.
	http.HandleFunc("/sendchat", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		msg := r.FormValue("msg")
		if msg == "" {
			http.Error(w, "Empty message", http.StatusBadRequest)
			return
		}
		err := host.chatConn.SendMessage(msg)
		if err != nil {
			http.Error(w, "Failed to send message", http.StatusInternalServerError)
			return
		}
		w.Write([]byte("Message sent"))
	})
	
	addr := ":" + strconv.Itoa(port)
	go func() {
		log.Printf("Host UI running at http://%s\n", hostURL)
		log.Printf("Scan the QR below to connect:\n%s\n", generateASCIIQRCode(signalingInfo))
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Fatal(err)
		}
	}()
	
	// Wait for an answer (i.e. a peer to connect).
	log.Println("Waiting for peer to connect...")
	<-host.answerGot
	
	// Now the data channel is established.
	log.Println("Peer connected! You can now send messages from the web UI (http://localhost:" + strconv.Itoa(port) + "/)")
	// Block forever.
	select {}
}

// HTML template for the host UI.
const hostHTML = `
<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <title>P2P Chat Host</title>
</head>
<body>
  <h1>P2P Chat Host</h1>
  <p>Scan the QR code below with a peer device:</p>
  <pre>{{.QR}}</pre>
  <p>Or copy the following signaling info:</p>
  <textarea rows="4" cols="80" readonly>{{.SignalingInfo}}</textarea>
  <hr>
  <h2>Send Chat Message</h2>
  <form id="chatForm">
      <input type="text" id="msg" size="60" autocomplete="off" placeholder="Your message...">
      <button type="submit">Send</button>
  </form>
  <script>
    document.getElementById("chatForm").addEventListener("submit", function(e){
      e.preventDefault();
      const msg = document.getElementById("msg").value;
      if(!msg) return;
      fetch("/sendchat", {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: "msg=" + encodeURIComponent(msg)
      }).then(() => {
        document.getElementById("msg").value = "";
      });
    });
  </script>
</body>
</html>
`

// -------------------------------
// Peer Implementation
// -------------------------------

func runPeer(offerB64 string, signalURL string) error {
	// Decode the offer SDP.
	offerBytes, err := base64.StdEncoding.DecodeString(offerB64)
	if err != nil {
		return fmt.Errorf("failed to decode offer: %v", err)
	}
	offerSDP := string(offerBytes)
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}
	
	// Create a new PeerConnection.
	webrtcSetting := webrtc.SettingEngine{}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(webrtcSetting))
	config := webrtc.Configuration{}
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("Error creating PeerConnection: %v", err)
	}
	
	// Set remote description.
	err = pc.SetRemoteDescription(offer)
	if err != nil {
		return fmt.Errorf("Error setting remote description: %v", err)
	}
	
	// Prepare to receive the data channel.
	var chatConn *ChatConn
	pc.OnDataChannel(func(d *webrtc.DataChannel) {
		log.Printf("DataChannel '%s'-'%d' received\n", d.Label(), d.ID())
		chatConn = &ChatConn{
			DC: d,
			OnMessage: func(msg string) {
				log.Printf("Received: %s", msg)
			},
		}
		d.OnOpen(func() {
			log.Println("DataChannel open!")
		})
		d.OnMessage(func(msg webrtc.DataChannelMessage) {
			chatConn.OnMessage(string(msg.Data))
		})
	})
	
	// Create an answer.
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("Error creating answer: %v", err)
	}
	err = pc.SetLocalDescription(answer)
	if err != nil {
		return fmt.Errorf("Error setting local description: %v", err)
	}
	// Wait for ICE gathering to complete.
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	<-gatherComplete
	
	answerSDP := pc.LocalDescription().SDP
	answerB64 := base64.StdEncoding.EncodeToString([]byte(answerSDP))
	// Prepare signaling message.
	msg := SignalingMessage{
		SDP: answerB64,
	}
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("JSON marshal error: %v", err)
	}
	
	// Send the answer to the host’s /answer endpoint.
	resp, err := http.Post(signalURL, "application/json", bytes.NewReader(msgJSON))
	if err != nil {
		return fmt.Errorf("failed to send answer: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("signaling error: %s", string(bodyBytes))
	}
	log.Println("Answer sent to host successfully. Awaiting data channel open...")
	
	// Block until the connection is open.
	// (For simplicity we wait 2 seconds; in production, use proper synchronization.)
	time.Sleep(2 * time.Second)
	
	// Simple command-line loop to send messages.
	fmt.Println("Enter messages to send; type 'exit' to quit.")
	for {
		fmt.Print("> ")
		var line string
		_, err := fmt.Scanln(&line)
		if err != nil {
			break
		}
		if strings.TrimSpace(line) == "exit" {
			break
		}
		if chatConn != nil && chatConn.DC != nil {
			err = chatConn.SendMessage(line)
			if err != nil {
				log.Println("Failed to send message:", err)
			}
		} else {
			log.Println("Data channel not established yet.")
		}
	}
	return nil
}

// -------------------------------
// Main entry point
// -------------------------------

func main() {
	mode := flag.String("mode", "host", "Run in host or peer mode")
	port := flag.Int("port", 8080, "Port for host mode (HTTP signaling UI)")
	offerFlag := flag.String("offer", "", "Base64 SDP offer (for peer mode)")
	signalFlag := flag.String("signal", "", "Signaling URL for sending answer (for peer mode)")
	flag.Parse()
	
	if *mode == "host" {
		log.Println("Starting in host mode...")
		if err := runHost(*port); err != nil {
			log.Fatal(err)
		}
	} else if *mode == "peer" {
		if *offerFlag == "" || *signalFlag == "" {
			log.Fatal("Peer mode requires -offer and -signal parameters.")
		}
		log.Println("Starting in peer mode...")
		if err := runPeer(*offerFlag, *signalFlag); err != nil {
			log.Fatal(err)
		}
	} else {
		log.Fatal("Invalid mode. Use -mode=host or -mode=peer")
	}
}
