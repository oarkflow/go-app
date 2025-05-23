// network_darwin.go
//go:build darwin
// +build darwin

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	bpfDevicePattern = "/dev/bpf%d"
	ifaceName        = "en0" // change as needed, e.g. "en1"
	bufSize          = 4096
)

// Darwin uses BPF devices under /dev/bpf*
func networkSniffer() {
	var fd int
	var err error
	// find an available /dev/bpfX
	for i := 0; i < 255; i++ {
		path := fmt.Sprintf(bpfDevicePattern, i)
		fd, err = syscall.Open(path, syscall.O_RDWR, 0)
		if err == nil {
			break
		}
		if !os.IsPermission(err) && !os.IsNotExist(err) {
			continue
		}
	}
	if err != nil {
		fmt.Println("networkSniffer: no BPF device available:", err)
		return
	}
	defer syscall.Close(fd)

	// set interface
	var ifr [16]byte
	copy(ifr[:], ifaceName)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.BIOCSETIF),
		uintptr(unsafe.Pointer(&ifr))); errno != 0 {
		fmt.Println("networkSniffer: BIOCSETIF error:", errno)
		return
	}
	// immediate mode
	opt := 1
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.BIOCIMMEDIATE),
		uintptr(unsafe.Pointer(&opt))); errno != 0 {
		fmt.Println("networkSniffer: BIOCIMMEDIATE error:", errno)
		return
	}
	// no header complete—deliver full ethernet frame
	hdrComplete := 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.BIOCSHDRCMPLT),
		uintptr(unsafe.Pointer(&hdrComplete))); errno != 0 {
		fmt.Println("networkSniffer: BIOCSHDRCMPLT error:", errno)
		return
	}

	buf := make([]byte, bufSize)
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil {
			continue
		}
		// BPF delivers a header before each packet—skip it
		// first 4 bytes: struct bpf_hdr bh_hdrlen at offset 2
		if n < 14+4 {
			continue
		}
		// get header length
		hdrlen := int(binary.LittleEndian.Uint16(buf[2:4]))
		if hdrlen+14 > n {
			continue
		}
		pkt := buf[hdrlen:n]
		go analyzePacket(pkt)
	}
}
