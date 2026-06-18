package services

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"

	"baremetal-platform/backend/internal/config"
)

func TestPXETFTPDataServesDynamicIPXEScriptAndRejectsTraversal(t *testing.T) {
	service := PXEService{cfg: config.Config{BootBaseURL: "http://boot.example.com", BootTFTPRoot: t.TempDir()}}

	data, err := service.tftpData("boot.ipxe")
	if err != nil {
		t.Fatalf("dynamic boot.ipxe: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "#!ipxe") || !strings.Contains(text, "set base-url http://boot.example.com") || !strings.Contains(text, "chain ${base-url}/boot/ipxe") || !strings.Contains(text, "${net0/mac}") {
		t.Fatalf("unexpected dynamic iPXE script: %s", text)
	}

	if _, err := service.tftpData("../secret"); err == nil {
		t.Fatalf("expected traversal filename to be rejected")
	}
}

func TestPXEServiceStartsListenersOnEphemeralPorts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := NewPXEService(nil, config.Config{
		BootServicesEnabled:  true,
		BootServiceMode:      "proxy",
		BootBindInterface:    "test-lab",
		BootDHCPListenAddr:   "127.0.0.1:0",
		BootDHCPServerIP:     "192.168.100.10",
		BootTFTPListenAddr:   "127.0.0.1:0",
		BootTFTPRoot:         t.TempDir(),
		BootTFTPBootfileUEFI: "ipxe.efi",
		BootTFTPBootfileBIOS: "undionly.kpxe",
		BootBaseURL:          "http://boot.example.com",
	}, t.Logf)
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start pxe listeners: %v", err)
	}
}

func TestParseDHCPPacketAndBootfileSelection(t *testing.T) {
	packetBytes := minimalDHCPDiscover(t, []byte{0x52, 0x54, 0x00, 0xaa, 0xbb, 0xcc}, 9)
	packet, err := parseDHCPPacket(packetBytes)
	if err != nil {
		t.Fatalf("parse dhcp packet: %v", err)
	}
	if packet.macString() != "52:54:00:aa:bb:cc" {
		t.Fatalf("unexpected mac %q", packet.macString())
	}
	if !packet.isPXEClient() {
		t.Fatalf("expected PXE client vendor class")
	}
	if packet.firmware() != "uefi" {
		t.Fatalf("expected uefi firmware")
	}

	service := PXEService{cfg: config.Config{BootTFTPBootfileUEFI: "ipxe.efi", BootTFTPBootfileBIOS: "undionly.kpxe"}}
	if got := service.bootfile(packet); got != "ipxe.efi" {
		t.Fatalf("expected UEFI bootfile, got %q", got)
	}

	biosPacket, err := parseDHCPPacket(minimalDHCPDiscover(t, []byte{0x52, 0x54, 0x00, 0xaa, 0xbb, 0xcd}, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := service.bootfile(biosPacket); got != "undionly.kpxe" {
		t.Fatalf("expected BIOS bootfile, got %q", got)
	}
}

func minimalDHCPDiscover(t *testing.T, mac []byte, arch uint16) []byte {
	t.Helper()
	if len(mac) != 6 {
		t.Fatalf("test mac must have 6 bytes")
	}
	packet := make([]byte, 240)
	packet[0] = 1
	packet[1] = 1
	packet[2] = 6
	packet[4], packet[5], packet[6], packet[7] = 0x12, 0x34, 0x56, 0x78
	copy(packet[28:34], mac)
	packet[236], packet[237], packet[238], packet[239] = 99, 130, 83, 99
	options := bytes.Buffer{}
	options.Write([]byte{53, 1, 1})
	options.Write([]byte{60, byte(len("PXEClient"))})
	options.WriteString("PXEClient")
	options.Write([]byte{93, 2, byte(arch >> 8), byte(arch)})
	options.WriteByte(255)
	packet = append(packet, options.Bytes()...)
	if _, err := net.ParseMAC(net.HardwareAddr(mac).String()); err != nil {
		t.Fatalf("test mac invalid: %v", err)
	}
	return packet
}
