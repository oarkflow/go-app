// network_windows.go
//go:build windows
// +build windows

package main

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

const (
	recvBufSize = 65536
	SIO_RCVALL  = 0x98000001
)

func networkSniffer() {
	// create raw socket for IP-level capture
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_IP)
	if err != nil {
		fmt.Println("networkSniffer: socket error:", err)
		return
	}
	defer syscall.Closesocket(fd)

	// bind to all interfaces (0.0.0.0)
	sa := &syscall.SockaddrInet4{Port: 0}
	copy(sa.Addr[:], net.ParseIP("0.0.0.0").To4())
	if err := syscall.Bind(fd, sa); err != nil {
		fmt.Println("networkSniffer: bind error:", err)
		return
	}

	// enable RCVALL = capture all IP packets
	var in uint32 = 1
	if err := syscall.Setsockopt(fd, syscall.IPPROTO_IP, SIO_RCVALL, (*byte)(unsafe.Pointer(&in)), 4); err != nil {
		fmt.Println("networkSniffer: Setsockopt RCVALL error:", err)
		// not fatal; we can still try to read
	}

	buf := make([]byte, recvBufSize)
	for {
		n, from, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			continue
		}
		// skip IPv6 or too-small packets
		if n < 20 {
			continue
		}
		// extract src IP
		// IPv4 header: src at bytes 12–16
		src := net.IP(buf[12:16]).String()
		// prepend slice and pass full IP packet
		go analyzePacket(buf[:n])

		_ = from // we already extracted src via analyzePacket
		_ = src
	}
}
