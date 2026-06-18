package services

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
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

func TestPXETFTPListenerServesBootIPXEOverUDP(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen test TFTP: %v", err)
	}
	defer conn.Close()
	service := PXEService{cfg: config.Config{BootBaseURL: "http://boot.example.com", BootTFTPRoot: t.TempDir()}}
	go service.serveTFTP(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := ProbeTFTPFile(ctx, conn.LocalAddr().String(), "boot.ipxe", 64*1024)
	if err != nil {
		t.Fatalf("probe TFTP boot.ipxe: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "#!ipxe") || !strings.Contains(text, "set base-url http://boot.example.com") {
		t.Fatalf("unexpected TFTP boot script: %s", text)
	}
}

func TestPXEDHCPListenerRespondsToPXEProbeOverUDP(t *testing.T) {
	db := newPXETestDB(t)
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen test DHCP: %v", err)
	}
	defer conn.Close()
	service := NewPXEService(db, config.Config{
		BootServiceMode:      "proxy",
		BootDHCPServerIP:     "192.168.100.10",
		BootTFTPBootfileUEFI: "ipxe.efi",
		BootTFTPBootfileBIOS: "undionly.kpxe",
		BootBaseURL:          "http://boot.example.com",
	}, t.Logf)
	go service.serveDHCP(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := ProbePXEDHCP(ctx, conn.LocalAddr().String(), "52:54:00:aa:bb:ee", 9)
	if err != nil {
		t.Fatalf("probe DHCP listener: %v", err)
	}
	if result.MessageType != 2 || result.Bootfile != "ipxe.efi" || result.ServerIP != "192.168.100.10" {
		t.Fatalf("unexpected DHCP probe result: %#v", result)
	}
	var bootEvents int64
	if err := db.Model(&models.BootEvent{}).Where("mac = ?", "52:54:00:aa:bb:ee").Count(&bootEvents).Error; err != nil {
		t.Fatalf("count boot events: %v", err)
	}
	if bootEvents != 1 {
		t.Fatalf("expected DHCP probe to record one boot event, got %d", bootEvents)
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

func newPXETestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Server{}, &models.ServerStatusHistory{}, &models.BootEvent{}, &models.Deployment{}, &models.MetadataToken{}, &models.NetworkConfig{}); err != nil {
		t.Fatalf("migrate PXE test DB: %v", err)
	}
	return db
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
