package main

import (
	"bufio"
	"encoding/base64"
	"flag"
	"fmt"
	"image"
	"os"
	"strings"

	"github.com/liyue201/goqr"
	"github.com/pion/webrtc/v3"
	"github.com/skip2/go-qrcode"
)

func main() {
	mode := flag.String("mode", "", "offer, answer, or scan")
	sdpFlag := flag.String("sdp", "", "offer SDP (base64 encoded)")
	fileFlag := flag.String("file", "offer.png", "PNG file containing offer QR")
	flag.Parse()

	switch *mode {
	case "offer":
		runOffer()
	case "answer":
		if *sdpFlag == "" {
			fmt.Println("-sdp parameter required for answer mode")
			os.Exit(1)
		}
		runAnswer(*sdpFlag)
	case "scan":
		s := scanPNG(*fileFlag)
		parts := strings.SplitN(s, "/", 3)
		if len(parts) < 3 {
			fmt.Println("Invalid QR content")
			os.Exit(1)
		}
		runAnswer(parts[2])
	default:
		fmt.Println("Usage:\n  -mode=offer\n  -mode=answer -sdp=<base64_SDP>\n  -mode=scan -file=<offer.png>")
		os.Exit(1)
	}

	select {}
}

func runOffer() {
	config := webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}}
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	// init channel for answer signaling
	initDC, err := pc.CreateDataChannel("init", nil)
	if err != nil {
		panic(err)
	}
	initDC.OnMessage(func(m webrtc.DataChannelMessage) {
		answer := webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  string(m.Data),
		}
		if err := pc.SetRemoteDescription(answer); err != nil {
			fmt.Println("Failed to set remote description:", err)
		} else {
			fmt.Println("✅ Answer applied. Waiting for chat …")
		}
	})

	// generate offer
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		panic(err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		panic(err)
	}
	<-webrtc.GatheringCompletePromise(pc)

	// prepare QR payload
	b64 := base64.StdEncoding.EncodeToString([]byte(pc.LocalDescription().SDP))
	qrURI := "golang-chat://scan/" + b64

	// write high-res PNG
	if err := qrcode.WriteFile(qrURI, qrcode.Low, 64, "offer.png"); err != nil { // changed error correction level to Low
		panic(err)
	}
	fmt.Println("Saved offer.png")

	// also print ASCII QR for direct scanning
	// fmt.Println("Scan this QR in your terminal:")
	// qrterminal.Generate(qrURI, qrterminal.L, os.Stdout)

	// fallback raw URL
	fmt.Printf("\n—or copy/paste this URL directly:\n%s\n\n", qrURI)

	// once guest opens "chat", handle it
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() == "chat" {
			fmt.Println("💬 Chat channel ready. Type messages or “/sendfile <path>”.")
			setupChat(dc, false)
		}
	})
}

func runAnswer(encoded string) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		panic(err)
	}
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(decoded),
	}

	config := webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}}
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case "init":
			dc.OnOpen(func() {
				// apply offer
				if err := pc.SetRemoteDescription(offer); err != nil {
					panic(err)
				}
				// create & send answer
				answer, err := pc.CreateAnswer(nil)
				if err != nil {
					panic(err)
				}
				if err := pc.SetLocalDescription(answer); err != nil {
					panic(err)
				}
				<-webrtc.GatheringCompletePromise(pc)
				dc.SendText(pc.LocalDescription().SDP)

				// then open chat
				chatDC, err := pc.CreateDataChannel("chat", nil)
				if err != nil {
					panic(err)
				}
				fmt.Println("✅ Connected! Sending initial request…")
				setupChat(chatDC, true)
			})
		}
	})
}

func setupChat(dc *webrtc.DataChannel, sendInitRequest bool) {
	dc.OnOpen(func() {
		if sendInitRequest {
			dc.SendText("REQ|guest_ready")
		}
		go func() {
			sc := bufio.NewScanner(os.Stdin)
			for sc.Scan() {
				line := sc.Text()
				switch {
				case strings.HasPrefix(line, "/sendfile "):
					path := strings.TrimSpace(line[len("/sendfile "):])
					data, err := os.ReadFile(path)
					if err != nil {
						fmt.Println("✖ read error:", err)
						continue
					}
					name := extractFilename(path)
					dc.SendText("FILE|" + name + "|" + base64.StdEncoding.EncodeToString(data))
				default:
					dc.SendText("MSG|" + line)
				}
			}
		}()
	})

	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		text := string(m.Data)
		switch {
		case strings.HasPrefix(text, "REQ|"):
			fmt.Println("▶️ Request:", text[4:])
		case strings.HasPrefix(text, "MSG|"):
			fmt.Println("Peer:", text[4:])
		case strings.HasPrefix(text, "FILE|"):
			parts := strings.SplitN(text, "|", 3)
			if len(parts) == 3 {
				payload, _ := base64.StdEncoding.DecodeString(parts[2])
				out := "recv_" + parts[1]
				if err := os.WriteFile(out, payload, 0644); err != nil {
					fmt.Println("✖ write error:", err)
				} else {
					fmt.Println("📁 Received file:", parts[1])
				}
			}
		}
	})
}

func extractFilename(path string) string {
	parts := strings.Split(path, string(os.PathSeparator))
	return parts[len(parts)-1]
}

func scanPNG(filename string) string {
	f, err := os.Open(filename)
	if err != nil {
		fmt.Println("Cannot open file:", err)
		os.Exit(1)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		fmt.Println("Image decode failed:", err)
		os.Exit(1)
	}
	codes, err := goqr.Recognize(img)
	if err != nil {
		fmt.Println("QR recognition error:", err)
		os.Exit(1)
	}
	if len(codes) == 0 {
		fmt.Println("No QR code found in", filename)
		os.Exit(1)
	}
	return string(codes[0].Payload)
}
