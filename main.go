// p2p_file_transfer.go
// A self-contained Go program for peer-to-peer file transfer using WebRTC (pion/webrtc)
// Supports:
//   - role=offer: send a file
//   - role=answer: receive a file
// Signaling is done via base64-encoded SDP printed/read on the terminal (manual copy-paste).

package main

import (
	"bufio"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/pion/webrtc/v3"
)

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func main() {
	role := flag.String("role", "", "Role: 'offer' to send, 'answer' to receive")
	filePath := flag.String("file", "", "File path to send (offer mode)")
	outPath := flag.String("out", "received.bin", "Output path to save received file (answer mode)")
	stun := flag.String("stun", "stun:stun.l.google.com:19302", "STUN server URL")
	// New flags for signaling
	signalMethod := flag.String("signal", "manual", "Signaling method: 'manual' or 'auto'")
	srvAddr := flag.String("srv", "localhost:12345", "Signaling server address for auto mode")
	flag.Parse()

	if *role != "offer" && *role != "answer" {
		flag.Usage()
		os.Exit(1)
	}

	// 1. Create PeerConnection
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{*stun}}},
	}
	pc, err := webrtc.NewPeerConnection(config)
	must(err)
	// Choose signaling method
	if *role == "offer" {
		if *signalMethod == "auto" {
			runOfferAuto(pc, *filePath, *srvAddr)
		} else {
			runOffer(pc, *filePath)
		}
	} else {
		if *signalMethod == "auto" {
			runAnswerAuto(pc, *outPath, *srvAddr)
		} else {
			runAnswer(pc, *outPath)
		}
	}

	// Block forever
	select {}
}

func runOffer(pc *webrtc.PeerConnection, path string) {
	// 2. Create DataChannel
	dc, err := pc.CreateDataChannel("file", nil)
	must(err)

	// On open, start streaming the file
	dc.OnOpen(func() {
		fmt.Println("DataChannel open, sending file...")
		sendFile(dc, path)
		dc.Close()
		pc.Close()
		fmt.Println("File sent successfully")
		os.Exit(0)
	})

	// Signaling
	offer, err := pc.CreateOffer(nil)
	must(err)
	must(pc.SetLocalDescription(offer))
	<-webrtc.GatheringCompletePromise(pc)

	printSDP("OFFER", pc.LocalDescription().SDP)

	// Read remote answer
	answerSDP := readSDPFromStdin("ANSWER")
	must(pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP}))
}

// New function: automatic signaling for the offer role.
func runOfferAuto(pc *webrtc.PeerConnection, path, srv string) {
	// Create DataChannel as in runOffer
	dc, err := pc.CreateDataChannel("file", nil)
	must(err)
	dc.OnOpen(func() {
		fmt.Println("DataChannel open, sending file...")
		sendFile(dc, path)
		dc.Close()
		pc.Close()
		fmt.Println("File sent successfully")
		os.Exit(0)
	})

	offer, err := pc.CreateOffer(nil)
	must(err)
	must(pc.SetLocalDescription(offer))
	<-webrtc.GatheringCompletePromise(pc)

	// Auto signaling: listen for remote connection
	ln, err := net.Listen("tcp", srv)
	must(err)
	fmt.Println("Waiting for remote connection on", srv)
	conn, err := ln.Accept()
	must(err)
	defer conn.Close()
	// Send offer SDP (base64 encoded with newline termination)
	offerEncoded := base64.StdEncoding.EncodeToString([]byte(pc.LocalDescription().SDP))
	_, err = fmt.Fprintf(conn, "%s\n", offerEncoded)
	must(err)
	// Read answer SDP
	answerEncoded, err := bufio.NewReader(conn).ReadString('\n')
	must(err)
	answerSDPBytes, err := base64.StdEncoding.DecodeString(answerEncoded[:len(answerEncoded)-1])
	must(err)
	must(pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: string(answerSDPBytes)}))
}

func runAnswer(pc *webrtc.PeerConnection, outPath string) {
	var file *os.File

	// Ensure output directory exists
	dir := filepath.Dir(outPath)
	if dir != "." && dir != "" {
		must(os.MkdirAll(dir, 0755))
	}

	// 2. Listen for DataChannel
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() {
			fmt.Println("DataChannel open, ready to receive file...")
			var err error
			file, err = os.Create(outPath)
			must(err)
		})

		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			_, err := file.Write(msg.Data)
			must(err)
		})

		dc.OnClose(func() {
			file.Sync()
			file.Close()
			fmt.Println("File received and saved to", outPath)
			pc.Close()
			os.Exit(0)
		})
	})

	// Signaling
	offerSDP := readSDPFromStdin("OFFER")
	must(pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}))

	answer, err := pc.CreateAnswer(nil)
	must(err)
	must(pc.SetLocalDescription(answer))
	<-webrtc.GatheringCompletePromise(pc)

	printSDP("ANSWER", pc.LocalDescription().SDP)
}

// New function: automatic signaling for the answer role.
func runAnswerAuto(pc *webrtc.PeerConnection, outPath, srv string) {
	// Ensure output directory exists
	dir := filepath.Dir(outPath)
	if dir != "." && dir != "" {
		must(os.MkdirAll(dir, 0755))
	}

	// DataChannel handler as in runAnswer
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		var file *os.File
		dc.OnOpen(func() {
			fmt.Println("DataChannel open, ready to receive file...")
			var err error
			file, err = os.Create(outPath)
			must(err)
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			_, err := file.Write(msg.Data)
			must(err)
		})
		dc.OnClose(func() {
			file.Sync()
			file.Close()
			fmt.Println("File received and saved to", outPath)
			pc.Close()
			os.Exit(0)
		})
	})

	// Dial to the offer's signaling address
	conn, err := net.Dial("tcp", srv)
	must(err)
	defer conn.Close()
	// Read offer SDP
	offerEncoded, err := bufio.NewReader(conn).ReadString('\n')
	must(err)
	offerSDPBytes, err := base64.StdEncoding.DecodeString(offerEncoded[:len(offerEncoded)-1])
	must(err)
	must(pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: string(offerSDPBytes)}))

	answer, err := pc.CreateAnswer(nil)
	must(err)
	must(pc.SetLocalDescription(answer))
	<-webrtc.GatheringCompletePromise(pc)

	// Send answer SDP
	answerEncoded := base64.StdEncoding.EncodeToString([]byte(pc.LocalDescription().SDP))
	_, err = fmt.Fprintf(conn, "%s\n", answerEncoded)
	must(err)
}

func sendFile(dc *webrtc.DataChannel, path string) {
	f, err := os.Open(path)
	must(err)
	defer f.Close()

	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		if err == io.EOF {
			break
		}
		must(err)
		must(dc.Send(buf[:n]))
		// slight pause to avoid buffer overflow in slow links
		time.Sleep(10 * time.Millisecond)
	}
}

// printSDP encodes the SDP in base64 and prints with headers
func printSDP(label, sdp string) {
	raw := base64.StdEncoding.EncodeToString([]byte(sdp))
	fmt.Printf("-----%s-----\n%s\n-----%s-----\n", label, raw, label)
}

// readSDPFromStdin reads base64 SDP from stdin between headers
func readSDPFromStdin(label string) string {
	r := bufio.NewReader(os.Stdin)
	fmt.Printf("Paste %s SDP (base64) and hit enter:\n", label)
	line, err := r.ReadString('\n')
	must(err)
	decoded, err := base64.StdEncoding.DecodeString(line)
	must(err)
	return string(decoded)
}
