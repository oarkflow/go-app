package main

// import (
// 	"bufio"
// 	"encoding/base64"
// 	"flag"
// 	"fmt"
// 	"image"
// 	"os"
// 	"strings"
//

// 	"github.com/liyue201/goqr"
// 	"github.com/pion/webrtc/v3"
// 	"github.com/skip2/go-qrcode"
// )

// func main() {
// 	mode := flag.String("mode", "", "offer, answer, or scan")
// 	sdpFlag := flag.String("sdp", "", "offer SDP (base64 encoded)")
// 	fileFlag := flag.String("file", "offer.png", "PNG file containing offer QR")
// 	flag.Parse()

// 	switch *mode {
// 	case "offer":
// 		runOffer()
// 	case "answer":
// 		if *sdpFlag == "" {
// 			fmt.Println("-sdp parameter required for answer mode")
// 			os.Exit(1)
// 		}
// 		runAnswer(*sdpFlag)
// 	case "scan":
// 		s := scanPNG(*fileFlag)
// 		parts := strings.SplitN(s, "/", 3)
// 		if len(parts) < 3 {
// 			fmt.Println("Invalid QR content")
// 			os.Exit(1)
// 		}
// 		sdpB64 := parts[2]
// 		runAnswer(sdpB64)
// 	default:
// 		fmt.Println("Usage:\n  -mode=offer\n  -mode=answer -sdp=<base64_SDP>\n  -mode=scan -file=<offer.png>")
// 		os.Exit(1)
// 	}

// 	select {}
// }

// func runOffer() {
// 	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
// 	if err != nil {
// 		panic(err)
// 	}

// 	// Create init data-channel for signaling answer
// 	initDC, err := pc.CreateDataChannel("init", nil)
// 	if err != nil {
// 		panic(err)
// 	}

// 	// On receiving answer SDP via init channel
// 	initDC.OnMessage(func(m webrtc.DataChannelMessage) {
// 		ans := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: string(m.Data)}
// 		if err := pc.SetRemoteDescription(ans); err != nil {
// 			fmt.Println("Failed to set remote description:", err)
// 		}
// 		fmt.Println("Answer received, remote description set. Waiting for guest request...")
// 	})

// 	// Create offer
// 	offer, err := pc.CreateOffer(nil)
// 	if err != nil {
// 		panic(err)
// 	}

// 	// Set local description and wait for ICE gathering
// 	if err := pc.SetLocalDescription(offer); err != nil {
// 		panic(err)
// 	}
// 	<-webrtc.GatheringCompletePromise(pc)

// 	// Encode SDP and create QR
// 	b64 := base64.StdEncoding.EncodeToString([]byte(pc.LocalDescription().SDP))
// 	qrData := "golang-chat://scan/" + b64
// 	if err := qrcode.WriteFile(qrData, qrcode.Medium, 256, "offer.png"); err != nil {
// 		panic(err)
// 	}
// 	fmt.Println("QR code saved as offer.png. Awaiting answer...")

// 	// Handle chat channel when opened by guest
// 	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
// 		if dc.Label() == "chat" {
// 			fmt.Println("Chat channel established. You can now chat and send files.")
// 			setupChat(dc, false)
// 		}
// 	})
// }

// func runAnswer(encoded string) {
// 	// Decode offer
// 	dec, err := base64.StdEncoding.DecodeString(encoded)
// 	if err != nil {
// 		panic(err)
// 	}
// 	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: string(dec)}

// 	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
// 	if err != nil {
// 		panic(err)
// 	}

// 	// Listen for init data-channel from offerer
// 	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
// 		if dc.Label() == "init" {
// 			dc.OnOpen(func() {
// 				// Set remote offer
// 				if err := pc.SetRemoteDescription(offer); err != nil {
// 					panic(err)
// 				}

// 				// Create and send answer
// 				answer, err := pc.CreateAnswer(nil)
// 				if err != nil {
// 					panic(err)
// 				}
// 				if err := pc.SetLocalDescription(answer); err != nil {
// 					panic(err)
// 				}
// 				<-webrtc.GatheringCompletePromise(pc)
// 				dc.SendText(pc.LocalDescription().SDP)

// 				// Now create chat channel
// 				chatDC, err := pc.CreateDataChannel("chat", nil)
// 				if err != nil {
// 					panic(err)
// 				}
// 				fmt.Println("Connected to offerer. You can now send requests, chat and files.")
// 				setupChat(chatDC, true)
// 			})
// 		}
// 	})
// }

// // setupChat manages messaging and file transfer on a data-channel.
// func setupChat(dc *webrtc.DataChannel, sendInitRequest bool) {
// 	dc.OnOpen(func() {
// 		if sendInitRequest {
// 			// Send initial request to offerer
// 			dc.SendText("REQ|guest_ready")
// 		}
// 		go func() {
// 			sc := bufio.NewScanner(os.Stdin)
// 			for sc.Scan() {
// 				t := sc.Text()
// 				if strings.HasPrefix(t, "/sendfile ") {
// 					path := strings.TrimSpace(t[len("/sendfile "):])
// 					b, err := os.ReadFile(path)
// 					if err != nil {
// 						fmt.Println("Error reading file:", err)
// 						continue
// 					}
// 					name := extractFilename(path)
// 					dc.SendText("FILE|" + name + "|" + base64.StdEncoding.EncodeToString(b))
// 				} else {
// 					dc.SendText("MSG|" + t)
// 				}
// 			}
// 		}()
// 	})

// 	dc.OnMessage(func(m webrtc.DataChannelMessage) {
// 		s := string(m.Data)
// 		switch {
// 		case strings.HasPrefix(s, "REQ|"):
// 			fmt.Println("Offerer request:", s[4:])
// 		case strings.HasPrefix(s, "MSG|"):
// 			fmt.Println("Peer:", s[4:])
// 		case strings.HasPrefix(s, "FILE|"):
// 			parts := strings.SplitN(s, "|", 3)
// 			if len(parts) == 3 {
// 				b, err := base64.StdEncoding.DecodeString(parts[2])
// 				if err != nil {
// 					fmt.Println("Failed to decode file:", err)
// 					return
// 				}
// 				out := "recv_" + parts[1]
// 				if err := os.WriteFile(out, b, 0644); err != nil {
// 					fmt.Println("Failed to write file:", err)
// 					return
// 				}
// 				fmt.Println("Received file:", parts[1])
// 			}
// 		}
// 	})
// }

// func extractFilename(path string) string {
// 	parts := strings.Split(path, string(os.PathSeparator))
// 	return parts[len(parts)-1]
// }

// func scanPNG(filename string) string {
// 	f, _ := os.Open(filename)
// 	defer f.Close()
// 	img, _, _ := image.Decode(f)
// 	qrCodes, _ := goqr.Recognize(img)
// 	if len(qrCodes) == 0 {
// 		fmt.Println("No QR code found", filename)
// 		os.Exit(1)
// 	}
// 	return string(qrCodes[0].Payload)
// }
