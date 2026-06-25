package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/url"
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
	if !strings.Contains(text, "#!ipxe") || !strings.Contains(text, "dhcp || shell") || !strings.Contains(text, "set base-url http://boot.example.com") || !strings.Contains(text, "chain ${base-url}/boot/ipxe") || !strings.Contains(text, "${net0/mac}") {
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

func TestPXETFTPListenerNegotiatesCommonOptions(t *testing.T) {
	serverConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen test TFTP: %v", err)
	}
	defer serverConn.Close()
	service := PXEService{cfg: config.Config{BootBaseURL: "http://boot.example.com", BootTFTPRoot: t.TempDir()}}
	go service.serveTFTP(serverConn)

	clientConn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		t.Fatalf("listen client UDP: %v", err)
	}
	defer clientConn.Close()

	req := []byte{0, 1}
	for _, part := range []string{"boot.ipxe", "octet", "blksize", "1024", "timeout", "1", "tsize", "0"} {
		req = append(req, []byte(part)...)
		req = append(req, 0)
	}
	serverAddr, err := net.ResolveUDPAddr("udp4", serverConn.LocalAddr().String())
	if err != nil {
		t.Fatalf("resolve TFTP server: %v", err)
	}
	if _, err := clientConn.WriteToUDP(req, serverAddr); err != nil {
		t.Fatalf("send RRQ: %v", err)
	}

	buf := make([]byte, 1500)
	_ = clientConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, transferAddr, err := clientConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read OACK: %v", err)
	}
	if n < 2 || binary.BigEndian.Uint16(buf[:2]) != 6 {
		t.Fatalf("expected OACK, got %v", buf[:n])
	}
	options := parseTFTPOACKOptions(t, buf[:n])
	if options["blksize"] != "1024" || options["timeout"] != "1" {
		t.Fatalf("expected blksize/timeout OACK options, got %#v", options)
	}
	if options["tsize"] == "" || options["tsize"] == "0" {
		t.Fatalf("expected non-zero tsize OACK option, got %#v", options)
	}
	if err := sendTFTPACK(clientConn, transferAddr, 0); err != nil {
		t.Fatalf("ack OACK: %v", err)
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, from, err := clientConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read DATA: %v", err)
	}
	if from.String() != transferAddr.String() {
		t.Fatalf("expected DATA from transfer addr %s, got %s", transferAddr, from)
	}
	if n < 4 || binary.BigEndian.Uint16(buf[:2]) != 3 || binary.BigEndian.Uint16(buf[2:4]) != 1 {
		t.Fatalf("expected DATA block 1, got %v", buf[:n])
	}
	if len(buf[4:n]) > 1024 {
		t.Fatalf("expected DATA payload within negotiated block size, got %d", len(buf[4:n]))
	}
	if !strings.Contains(string(buf[4:n]), "#!ipxe") {
		t.Fatalf("expected boot.ipxe payload, got %q", string(buf[4:n]))
	}
	if err := sendTFTPACK(clientConn, transferAddr, 1); err != nil {
		t.Fatalf("ack DATA: %v", err)
	}
}

func TestProbeTFTPFileWithOptionsNegotiatesOACK(t *testing.T) {
	serverConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen test TFTP: %v", err)
	}
	defer serverConn.Close()
	service := PXEService{cfg: config.Config{BootBaseURL: "http://boot.example.com", BootTFTPRoot: t.TempDir()}}
	go service.serveTFTP(serverConn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, options, err := ProbeTFTPFileWithOptions(ctx, serverConn.LocalAddr().String(), "boot.ipxe", 64*1024, map[string]string{"blksize": "1024", "timeout": "1", "tsize": "0"})
	if err != nil {
		t.Fatalf("probe TFTP boot.ipxe with options: %v", err)
	}
	if !strings.Contains(string(data), "#!ipxe") {
		t.Fatalf("expected boot.ipxe payload, got %q", string(data))
	}
	if options["blksize"] != "1024" || options["timeout"] != "1" || options["tsize"] == "" || options["tsize"] == "0" {
		t.Fatalf("expected negotiated OACK options, got %#v", options)
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
	if result.MessageType != 2 || result.Bootfile != "ipxe.efi" || result.ServerIP != "192.168.100.10" || result.NextServerIP != "192.168.100.10" || result.TFTPServerName != "192.168.100.10" {
		t.Fatalf("unexpected DHCP probe result: %#v", result)
	}
	if bootEvents := countBootEvents(t, db, "52:54:00:aa:bb:ee"); bootEvents != 0 {
		t.Fatalf("synthetic DHCP probe should not record boot events, got %d", bootEvents)
	}

	sendRawDHCPDiscover(t, conn.LocalAddr().String(), minimalDHCPDiscover(t, []byte{0x52, 0x54, 0x00, 0xaa, 0xbb, 0xef}, 9))
	if bootEvents := waitForBootEvents(t, db, "52:54:00:aa:bb:ef", 1); bootEvents != 1 {
		t.Fatalf("expected real PXE DHCP request to record one boot event, got %d", bootEvents)
	}
	var event models.BootEvent
	if err := db.Where("mac = ?", "52:54:00:aa:bb:ef").Order("id desc").First(&event).Error; err != nil {
		t.Fatalf("load DHCP boot event: %v", err)
	}
	if event.Source != "pxe_dhcp" {
		t.Fatalf("expected DHCP boot event source pxe_dhcp, got %#v", event)
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

func TestPXEBootfileReturnsHTTPBootScriptForIPXEClient(t *testing.T) {
	packet, err := parseDHCPPacket(ipxeDHCPDiscover(t, []byte{0x52, 0x54, 0x00, 0xaa, 0xbb, 0xce}, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !packet.isIPXEClient() {
		t.Fatalf("expected iPXE client detection")
	}
	service := PXEService{cfg: config.Config{
		BootBaseURL:          "http://boot.example.com",
		BootTFTPBootfileUEFI: "ipxe.efi",
		BootTFTPBootfileBIOS: "undionly.kpxe",
	}}
	got := service.bootfile(packet)
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse iPXE boot URL %q: %v", got, err)
	}
	if parsed.Scheme != "http" || parsed.Host != "boot.example.com" || parsed.Path != "/boot/ipxe" {
		t.Fatalf("expected HTTP /boot/ipxe URL, got %q", got)
	}
	if parsed.Query().Get("mac") != "52:54:00:aa:bb:ce" || parsed.Query().Get("arch") != "x86" || parsed.Query().Get("firmware") != "bios" {
		t.Fatalf("unexpected iPXE boot URL query: %q", got)
	}
}

func TestProxyDHCPAllowsIPXEClientWithoutPXEVendorClass(t *testing.T) {
	db := newPXETestDB(t)
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen test DHCP: %v", err)
	}
	defer conn.Close()
	service := NewPXEService(db, config.Config{
		BootServiceMode:      "proxy",
		BootDHCPServerIP:     "192.168.100.10",
		BootBaseURL:          "http://boot.example.com",
		BootTFTPBootfileUEFI: "ipxe.efi",
		BootTFTPBootfileBIOS: "undionly.kpxe",
	}, t.Logf)
	go service.serveDHCP(conn)

	response := sendDHCPAndReadResponse(t, conn.LocalAddr().String(), ipxeDHCPDiscover(t, []byte{0x52, 0x54, 0x00, 0xaa, 0xbb, 0xcf}, 0))
	if got := strings.TrimSpace(string(response.options[67])); !strings.HasPrefix(got, "http://boot.example.com/boot/ipxe?") {
		t.Fatalf("expected iPXE client to receive HTTP boot script, got %q", got)
	}
}

func TestProxyDHCPRespondsToPXEBootServerInform(t *testing.T) {
	db := newPXETestDB(t)
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen test PXE boot server: %v", err)
	}
	defer conn.Close()
	service := NewPXEService(db, config.Config{
		BootServiceMode:      "proxy",
		BootDHCPServerIP:     "192.168.100.10",
		BootBaseURL:          "http://boot.example.com",
		BootTFTPBootfileUEFI: "ipxe.efi",
		BootTFTPBootfileBIOS: "undionly.kpxe",
	}, t.Logf)
	go service.serveDHCP(conn)

	response := sendDHCPAndReadResponse(t, conn.LocalAddr().String(), pxeDHCPInform(t, []byte{0x52, 0x54, 0x00, 0xaa, 0xbb, 0xd0}, net.IPv4(192, 168, 100, 20), 0))
	if got := response.options[53]; len(got) != 1 || got[0] != dhcpAck {
		t.Fatalf("expected DHCPACK response to PXE boot server INFORM, got %#v", got)
	}
	if got := strings.TrimSpace(string(response.options[67])); got != "undionly.kpxe" {
		t.Fatalf("expected BIOS bootfile in PXE boot server response, got %q", got)
	}
}

func TestProxyDHCPBootServerListenAddr(t *testing.T) {
	got, err := ProxyDHCPBootServerListenAddr("192.168.100.10:67")
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.0.0.0:4011" {
		t.Fatalf("expected proxy boot server to listen on all interfaces for broadcast discovery, got %q", got)
	}
	got, err = ProxyDHCPBootServerListenAddr("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:0" {
		t.Fatalf("expected ephemeral test port to stay ephemeral, got %q", got)
	}
}

func TestBuildDHCPResponseIncludesOption67AndBOOTPFile(t *testing.T) {
	service := PXEService{cfg: config.Config{
		BootDHCPServerIP:     "192.168.100.10",
		BootTFTPBootfileUEFI: "ipxe.efi",
		BootTFTPBootfileBIOS: "undionly.kpxe",
	}}

	tests := []struct {
		name     string
		arch     uint16
		expected string
	}{
		{name: "uefi", arch: 9, expected: "ipxe.efi"},
		{name: "bios", arch: 0, expected: "undionly.kpxe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			packet, err := parseDHCPPacket(minimalDHCPDiscover(t, []byte{0x52, 0x54, 0x00, 0xaa, 0xbb, byte(tt.arch)}, tt.arch))
			if err != nil {
				t.Fatalf("parse dhcp packet: %v", err)
			}
			responseBytes := service.buildDHCPResponse(packet, net.IPv4zero, 2)
			response, err := parseDHCPResponse(responseBytes)
			if err != nil {
				t.Fatalf("parse dhcp response: %v", err)
			}
			if got := string(response.options[67]); got != tt.expected {
				t.Fatalf("expected option 67 bootfile %q, got %q", tt.expected, got)
			}
			if got := dhcpOptionIP(response.options[54]); got != "192.168.100.10" {
				t.Fatalf("expected option 54 server identifier 192.168.100.10, got %q", got)
			}
			if got := dhcpPacketIP(response.siaddr[:]); got != "192.168.100.10" {
				t.Fatalf("expected BOOTP next-server 192.168.100.10, got %q", got)
			}
			if got := string(response.options[66]); got != "192.168.100.10" {
				t.Fatalf("expected option 66 TFTP server name 192.168.100.10, got %q", got)
			}
			if got := response.legacyBootfile(); got != tt.expected {
				t.Fatalf("expected BOOTP file bootfile %q, got %q", tt.expected, got)
			}
			if got := bootpString(response.sname[:]); got != "192.168.100.10" {
				t.Fatalf("expected BOOTP server name to carry server IP, got %q", got)
			}
		})
	}
}

func TestProbePXEDHCPFallsBackToBOOTPFileField(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen test DHCP: %v", err)
	}
	defer conn.Close()
	service := PXEService{cfg: config.Config{
		BootServiceMode:      "proxy",
		BootDHCPServerIP:     "192.168.100.10",
		BootTFTPBootfileUEFI: "ipxe.efi",
		BootTFTPBootfileBIOS: "undionly.kpxe",
	}}
	go func() {
		buf := make([]byte, 1500)
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		packet, err := parseDHCPPacket(buf[:n])
		if err != nil {
			return
		}
		response := service.buildDHCPResponse(packet, net.IPv4zero, 2)
		response = removeDHCPOptionForTest(response, 67)
		_, _ = conn.WriteToUDP(response, addr)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := ProbePXEDHCP(ctx, conn.LocalAddr().String(), "52:54:00:aa:bb:fe", 0)
	if err != nil {
		t.Fatalf("probe DHCP listener: %v", err)
	}
	if result.Bootfile != "undionly.kpxe" || result.LegacyBootfile != "undionly.kpxe" {
		t.Fatalf("expected probe to use BOOTP file fallback, got %#v", result)
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

func parseTFTPOACKOptions(t *testing.T, data []byte) map[string]string {
	t.Helper()
	if len(data) < 2 || binary.BigEndian.Uint16(data[:2]) != 6 {
		t.Fatalf("not an OACK packet: %v", data)
	}
	parts := bytes.Split(data[2:], []byte{0})
	options := map[string]string{}
	for i := 0; i+1 < len(parts); i += 2 {
		key := strings.ToLower(strings.TrimSpace(string(parts[i])))
		if key == "" {
			continue
		}
		options[key] = strings.TrimSpace(string(parts[i+1]))
	}
	return options
}

func countBootEvents(t *testing.T, db *gorm.DB, mac string) int64 {
	t.Helper()
	var bootEvents int64
	if err := db.Model(&models.BootEvent{}).Where("mac = ?", mac).Count(&bootEvents).Error; err != nil {
		t.Fatalf("count boot events: %v", err)
	}
	return bootEvents
}

func waitForBootEvents(t *testing.T, db *gorm.DB, mac string, expected int64) int64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var bootEvents int64
	for time.Now().Before(deadline) {
		bootEvents = countBootEvents(t, db, mac)
		if bootEvents == expected {
			return bootEvents
		}
		time.Sleep(20 * time.Millisecond)
	}
	return bootEvents
}

func sendRawDHCPDiscover(t *testing.T, addr string, packet []byte) {
	t.Helper()
	_ = sendDHCPAndReadResponse(t, addr, packet)
}

func sendDHCPAndReadResponse(t *testing.T, addr string, packet []byte) dhcpPacket {
	t.Helper()
	serverAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		t.Fatalf("resolve DHCP server addr: %v", err)
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		t.Fatalf("listen DHCP client socket: %v", err)
	}
	defer conn.Close()
	if _, err := conn.WriteToUDP(packet, serverAddr); err != nil {
		t.Fatalf("send DHCP discover: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read DHCP response: %v", err)
	}
	response, err := parseDHCPResponse(buf[:n])
	if err != nil {
		t.Fatalf("parse DHCP response: %v", err)
	}
	return response
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

func ipxeDHCPDiscover(t *testing.T, mac []byte, arch uint16) []byte {
	t.Helper()
	if len(mac) != 6 {
		t.Fatalf("test mac must have 6 bytes")
	}
	packet := make([]byte, 240)
	packet[0] = 1
	packet[1] = 1
	packet[2] = 6
	packet[4], packet[5], packet[6], packet[7] = 0x12, 0x34, 0x56, 0x79
	copy(packet[28:34], mac)
	packet[236], packet[237], packet[238], packet[239] = 99, 130, 83, 99
	options := bytes.Buffer{}
	options.Write([]byte{53, 1, 1})
	options.Write([]byte{60, byte(len("iPXE"))})
	options.WriteString("iPXE")
	options.Write([]byte{77, byte(len("iPXE"))})
	options.WriteString("iPXE")
	options.Write([]byte{93, 2, byte(arch >> 8), byte(arch)})
	options.WriteByte(255)
	packet = append(packet, options.Bytes()...)
	if _, err := net.ParseMAC(net.HardwareAddr(mac).String()); err != nil {
		t.Fatalf("test mac invalid: %v", err)
	}
	return packet
}

func pxeDHCPInform(t *testing.T, mac []byte, ciaddr net.IP, arch uint16) []byte {
	t.Helper()
	packet := minimalDHCPDiscover(t, mac, arch)
	packet[242] = dhcpInform
	if ip := ciaddr.To4(); ip != nil {
		copy(packet[12:16], ip)
	}
	return packet
}

func removeDHCPOptionForTest(packet []byte, code byte) []byte {
	if len(packet) < 240 {
		return packet
	}
	out := append([]byte{}, packet[:240]...)
	for i := 240; i < len(packet); {
		option := packet[i]
		i++
		if option == 255 {
			out = append(out, 255)
			return out
		}
		if option == 0 {
			out = append(out, 0)
			continue
		}
		if i >= len(packet) {
			break
		}
		length := int(packet[i])
		i++
		if i+length > len(packet) {
			break
		}
		if option != code {
			out = append(out, option, byte(length))
			out = append(out, packet[i:i+length]...)
		}
		i += length
	}
	return append(out, 255)
}
