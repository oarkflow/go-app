//go:build linux
// +build linux

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	ifaceName = "eth0"
	snapLen   = 65536
	protoAll  = 0x0003 // ETH_P_ALL in host byte order
)

// real raw‐socket sniffer on Linux
func networkSniffer() {
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(protoAll)))
	if err != nil {
		fmt.Println("netSniffer socket error:", err)
		return
	}
	defer syscall.Close(fd)

	var ifr [16]byte
	copy(ifr[:], ifaceName)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.SIOCGIFINDEX),
		uintptr(unsafe.Pointer(&ifr)))
	if errno != 0 {
		fmt.Println("ioctl error:", errno)
		return
	}
	idx := *(*int32)(unsafe.Pointer(&ifr[15]))

	sll := &syscall.SockaddrLinklayer{Protocol: htons(protoAll), Ifindex: int(idx)}
	if err := syscall.Bind(fd, sll); err != nil {
		fmt.Println("bind error:", err)
		return
	}

	buf := make([]byte, snapLen)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			continue
		}
		go analyzePacket(buf[:n])
	}
}

// htons for Linux raw sockets
func htons(i uint16) uint16 {
	return (i<<8)&0xff00 | i>>8
}
