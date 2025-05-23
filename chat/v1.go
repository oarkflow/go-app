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
// 	if *mode == "offer" {
// 		runOffer()
// 	} else if *mode == "answer" && *sdpFlag != "" {
// 		runAnswer(*sdpFlag)
// 	} else if *mode == "scan" {
// 		s := scanPNG(*fileFlag)
// 		parts := strings.SplitN(s, "/", 3)
// 		if len(parts) < 3 {
// 			fmt.Println("Invalid QR content")
// 			os.Exit(1)
// 		}
// 		sdpB64 := parts[2]
// 		runAnswer(sdpB64)
// 	} else {
// 		fmt.Println("Usage:\n  -mode=offer\n  -mode=answer -sdp=<base64_SDP>\n  -mode=scan -file=<offer.png>")
// 		os.Exit(1)
// 	}
// 	select {}
// }

// func runOffer() {
// 	pc, _ := webrtc.NewPeerConnection(webrtc.Configuration{})
// 	initDC, _ := pc.CreateDataChannel("init", nil)
// 	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
// 		if dc.Label() == "chat" {
// 			setupChat(dc)
// 		}
// 	})
// 	initDC.OnMessage(func(m webrtc.DataChannelMessage) {
// 		ans := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: string(m.Data)}
// 		pc.SetRemoteDescription(ans)
// 	})
// 	offer, _ := pc.CreateOffer(nil)
// 	webrtc.GatheringCompletePromise(pc)
// 	pc.SetLocalDescription(offer)
// 	b64 := base64.StdEncoding.EncodeToString([]byte(offer.SDP))
// 	qrData := "golang-chat://scan/" + b64
// 	qrcode.WriteFile(qrData, qrcode.Medium, 256, "offer.png")
// 	fmt.Println("QR code saved as offer.png")
// }

// func runAnswer(encoded string) {
// 	dec, _ := base64.StdEncoding.DecodeString(encoded)
// 	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: string(dec)}
// 	pc, _ := webrtc.NewPeerConnection(webrtc.Configuration{})
// 	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
// 		if dc.Label() == "init" {
// 			dc.OnOpen(func() {
// 				answer, _ := pc.CreateAnswer(nil)
// 				webrtc.GatheringCompletePromise(pc)
// 				pc.SetLocalDescription(answer)
// 				dc.SendText(pc.LocalDescription().SDP)
// 				chatDC, _ := pc.CreateDataChannel("chat", nil)
// 				setupChat(chatDC)
// 			})
// 		}
// 	})
// 	pc.SetRemoteDescription(offer)
// }

// func setupChat(dc *webrtc.DataChannel) {
// 	dc.OnOpen(func() {
// 		go func() {
// 			sc := bufio.NewScanner(os.Stdin)
// 			for sc.Scan() {
// 				t := sc.Text()
// 				if strings.HasPrefix(t, "/sendfile ") {
// 					path := strings.TrimSpace(t[len("/sendfile "):])
// 					b, _ := os.ReadFile(path)
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
// 		if strings.HasPrefix(s, "MSG|") {
// 			fmt.Println("Peer:", s[4:])
// 		} else if strings.HasPrefix(s, "FILE|") {
// 			parts := strings.SplitN(s, "|", 3)
// 			if len(parts) == 3 {
// 				b, _ := base64.StdEncoding.DecodeString(parts[2])
// 				os.WriteFile("recv_"+parts[1], b, 0644)
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
// 		fmt.Println("No QR code found")
// 		os.Exit(1)
// 	}
// 	return string(qrCodes[0].Payload)
// }
