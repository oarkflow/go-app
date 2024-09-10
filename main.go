package main

import (
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// WebSocket upgrader to upgrade HTTP requests to WebSocket
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// A map to store connected peers
var peers = make(map[string]*websocket.Conn)
var peersLock sync.Mutex

// Message format
type Message struct {
	Type      string `json:"type"`
	From      string `json:"from"`
	To        string `json:"to"`
	SDP       string `json:"sdp,omitempty"`
	Candidate string `json:"candidate,omitempty"`
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Upgrade the connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade failed:", err)
		return
	}
	defer conn.Close()

	// Register the peer with a unique ID (e.g., from URL query or client-side)
	peerID := r.URL.Query().Get("id")
	peersLock.Lock()
	peers[peerID] = conn
	peersLock.Unlock()
	log.Println("Peer connected:", peerID)

	for {
		// Receive message from the peer
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println("Read error:", err)
			break
		}

		// Forward the message to the intended peer
		peersLock.Lock()
		targetConn, ok := peers[msg.To]
		peersLock.Unlock()

		if ok {
			err = targetConn.WriteJSON(msg)
			if err != nil {
				log.Println("Write error:", err)
			}
		} else {
			log.Println("Peer not found:", msg.To)
		}
	}

	// Remove peer on disconnection
	peersLock.Lock()
	delete(peers, peerID)
	peersLock.Unlock()
	log.Println("Peer disconnected:", peerID)
}

func main() {
	// WebSocket route
	http.HandleFunc("/ws", handleWebSocket)

	// Start the HTTP server
	log.Println("Signaling server started at :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal("ListenAndServe:", err)
	}
}
