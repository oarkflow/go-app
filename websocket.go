package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Peer struct {
	conn *websocket.Conn
}

var peers = make(map[string]*Peer)
var peersLock sync.Mutex

func signal(w http.ResponseWriter, r *http.Request) {
	peerID := r.URL.Query().Get("id")
	if peerID == "" {
		http.Error(w, "Missing peer ID", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	peersLock.Lock()
	peers[peerID] = &Peer{conn: conn}
	peersLock.Unlock()

	defer func() {
		peersLock.Lock()
		delete(peers, peerID)
		peersLock.Unlock()
		conn.Close()
	}()

	// Notify peer1 that peer2 has connected
	if peerID == "peer2" {
		peer1 := peers["peer1"]
		if peer1 != nil {
			err := peer1.conn.WriteJSON(map[string]interface{}{
				"peer2Connected": true,
			})
			if err != nil {
				log.Println("Error notifying peer1:", err)
			}
		}
	}

	for {
		var msg map[string]interface{}
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println("Error reading message:", err)
			break
		}

		toID := msg["to"].(string)
		peersLock.Lock()
		toPeer, exists := peers[toID]
		peersLock.Unlock()
		if !exists {
			log.Println("Peer not found:", toID)
			continue
		}

		err = toPeer.conn.WriteJSON(msg)
		if err != nil {
			log.Println("Error sending message:", err)
		}
	}
}

func main() {
	http.HandleFunc("/ws", signal)
	http.Handle("/", http.FileServer(http.Dir("./static")))

	fmt.Println("Server started at :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
