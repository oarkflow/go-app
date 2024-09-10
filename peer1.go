package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/faiface/mainthread"
	"github.com/faiface/pixel"
	"github.com/faiface/pixel/pixelgl"
	"github.com/gorilla/websocket"
	"github.com/pion/mediadevices"
	_ "github.com/pion/mediadevices/pkg/driver/camera"
	_ "github.com/pion/mediadevices/pkg/driver/microphone"
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media/ivfwriter"
)

// Message format for signaling
type Message struct {
	Type      string `json:"type"`
	From      string `json:"from"`
	To        string `json:"to"`
	SDP       string `json:"sdp,omitempty"`
	Candidate string `json:"candidate,omitempty"`
}

// WebSocket connection
var conn *websocket.Conn
var peerID string = "peer1" // Unique peer ID for this client

// Peer connection
var peerConnection *webrtc.PeerConnection

func main() {
	// Use mainthread library to open window for video display
	mainthread.Run(run)
}

func run() {
	// Connect to the signaling server
	connectToSignalingServer()

	// Create WebRTC peer connection
	var err error
	peerConnection, err = webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Fatal(err)
	}

	// Set ICE candidate handler
	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}

		// Send ICE candidate to signaling server
		msg := Message{
			Type:      "candidate",
			From:      peerID,
			To:        "peer2", // The target peer's ID
			Candidate: candidate.ToJSON().Candidate,
		}
		err := conn.WriteJSON(msg)
		if err != nil {
			log.Println("Failed to send ICE candidate:", err)
		}
	})

	// Handle incoming media tracks
	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		fmt.Println("Received track:", track.Kind())
		go saveTrackToFile(track)
	})

	// Start media capture and create the video preview window
	go startMediaCapture()

	// Create an offer and set local description
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		log.Fatal(err)
	}

	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		log.Fatal(err)
	}

	// Send the offer to the signaling server
	msg := Message{
		Type: "offer",
		From: peerID,
		To:   "peer2", // The target peer's ID
		SDP:  offer.SDP,
	}
	err = conn.WriteJSON(msg)
	if err != nil {
		log.Fatal("Failed to send offer:", err)
	}

	// Handle messages from signaling server
	go handleSignalingMessages()

	// Block main thread and run video display
	mainthread.Call(func() {
		pixelgl.Run(runVideoWindow)
	})
}

// Start video and audio capture, and add tracks to peer connection
func startMediaCapture() {
	// Capture video from the camera
	videoStream, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Video: func(c *mediadevices.MediaTrackConstraints) {
			c.Width = prop.Int(640)
			c.Height = prop.Int(480)
		},
	})
	if err != nil {
		log.Fatal("Failed to get video stream:", err)
	}

	// Capture audio from the microphone
	audioStream, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Audio: func(constraints *mediadevices.MediaTrackConstraints) {

		},
	})
	if err != nil {
		log.Fatal("Failed to get audio stream:", err)
	}

	// Add video track to peer connection
	for _, track := range videoStream.GetVideoTracks() {
		_, err := peerConnection.AddTrack(track)
		if err != nil {
			log.Fatal("Failed to add video track:", err)
		}
	}

	// Add audio track to peer connection
	for _, track := range audioStream.GetAudioTracks() {
		_, err := peerConnection.AddTrack(track)
		if err != nil {
			log.Fatal("Failed to add audio track:", err)
		}
	}
}

// WebSocket connection to signaling server
func connectToSignalingServer() {
	u := url.URL{Scheme: "ws", Host: "localhost:8080", Path: "/ws", RawQuery: "id=" + peerID}
	var err error
	conn, _, err = websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatal("WebSocket connection failed:", err)
	}
}

// Handle incoming signaling messages (SDP/ICE candidates)
func handleSignalingMessages() {
	for {
		// Receive message from signaling server
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println("Read error:", err)
			break
		}

		switch msg.Type {
		case "answer":
			// Set remote SDP description when answer is received
			err = peerConnection.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeAnswer,
				SDP:  msg.SDP,
			})
			if err != nil {
				log.Println("Failed to set remote description:", err)
			}
		case "candidate":
			// Wait for remote description before setting ICE candidate
			for peerConnection.RemoteDescription() == nil {
				time.Sleep(time.Millisecond * 100)
			}
			err = peerConnection.AddICECandidate(webrtc.ICECandidateInit{Candidate: msg.Candidate})
			if err != nil {
				log.Println("Failed to add ICE candidate:", err)
			}
		}
	}
}

// Video window to show webcam preview
func runVideoWindow() {
	cfg := pixelgl.WindowConfig{
		Title:  "Webcam Preview",
		Bounds: pixel.R(0, 0, 640, 480),
		VSync:  true,
	}

	win, err := pixelgl.NewWindow(cfg)
	if err != nil {
		panic(err)
	}

	for !win.Closed() {
		// Display webcam feed (you would need to get webcam frame buffer here)
		win.Clear(pixel.RGB(0, 0, 0)) // For now, it's just a black screen
		win.Update()
	}
}

// Save the incoming media track to file (for later playback)
func saveTrackToFile(track *webrtc.TrackRemote) {
	file, err := os.Create(fmt.Sprintf("output-%s.ivf", track.Kind()))
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	ivfWriter, err := ivfwriter.NewWith(file)
	if err != nil {
		log.Fatal(err)
	}
	defer ivfWriter.Close()

	for {
		rtpPacket, _, err := track.ReadRTP()
		if err != nil {
			log.Println("RTP read error:", err)
			return
		}
		ivfWriter.WriteRTP(rtpPacket)
	}
}
