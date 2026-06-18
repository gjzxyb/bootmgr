package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"gorm.io/gorm"
)

type PXEService struct {
	db     *gorm.DB
	cfg    config.Config
	boot   BootService
	logger func(string, ...any)
}

func NewPXEService(db *gorm.DB, cfg config.Config, logger func(string, ...any)) PXEService {
	if logger == nil {
		logger = func(string, ...any) {}
	}
	return PXEService{db: db, cfg: cfg, boot: NewBootService(db, cfg), logger: logger}
}

func (s PXEService) Start(ctx context.Context) error {
	if !s.cfg.BootServicesEnabled {
		s.logger("PXE/DHCP/TFTP services disabled")
		return nil
	}
	for _, issue := range config.BootRuntimeIssues(s.cfg) {
		s.logger("boot service %s [%s]: %s", issue.Level, issue.Key, issue.Message)
		if issue.Level == "error" {
			return fmt.Errorf("boot service runtime check failed: %s", issue.Message)
		}
	}
	if strings.ToLower(strings.TrimSpace(s.cfg.BootServiceMode)) != "external" {
		if err := s.startDHCP(ctx); err != nil {
			return err
		}
	}
	if err := s.startTFTP(ctx); err != nil {
		return err
	}
	return nil
}

func (s PXEService) startDHCP(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp4", s.cfg.BootDHCPListenAddr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("listen DHCP on %s: %w", s.cfg.BootDHCPListenAddr, err)
	}
	s.logger("DHCP/ProxyDHCP listener active on %s mode=%s interface=%s", s.cfg.BootDHCPListenAddr, s.cfg.BootServiceMode, s.cfg.BootBindInterface)
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go s.serveDHCP(conn)
	return nil
}

func (s PXEService) serveDHCP(conn *net.UDPConn) {
	buf := make([]byte, 1500)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.logger("DHCP read failed: %v", err)
			}
			return
		}
		packet, err := parseDHCPPacket(buf[:n])
		if err != nil {
			continue
		}
		go s.handleDHCPPacket(conn, addr, packet)
	}
}

func (s PXEService) handleDHCPPacket(conn *net.UDPConn, addr *net.UDPAddr, packet dhcpPacket) {
	msgType := packet.options[53]
	if len(msgType) == 0 || (msgType[0] != 1 && msgType[0] != 3) {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(s.cfg.BootServiceMode))
	if mode == "proxy" && !packet.isPXEClient() {
		return
	}
	responseType := byte(2)
	if msgType[0] == 3 {
		responseType = 5
	}
	leaseIP := net.IPv4(0, 0, 0, 0)
	if mode == "builtin" {
		leaseIP = s.leaseIP(packet)
		if leaseIP == nil {
			s.logger("DHCP request from %s could not receive a lease", packet.macString())
			return
		}
	}
	resp := s.buildDHCPResponse(packet, leaseIP, responseType)
	dest := &net.UDPAddr{IP: net.IPv4bcast, Port: 68}
	if addr != nil && addr.Port != 68 {
		dest = addr
	}
	if _, err := conn.WriteToUDP(resp, dest); err != nil {
		s.logger("DHCP response to %s failed: %v", dest, err)
		return
	}
	_, _, _ = s.boot.RenderIPXEScript(BootRequest{MAC: packet.macString(), Architecture: packet.architecture(), Firmware: packet.firmware(), RemoteAddr: addr.String()})
}

func (s PXEService) buildDHCPResponse(packet dhcpPacket, leaseIP net.IP, messageType byte) []byte {
	resp := make([]byte, 240)
	resp[0] = 2
	resp[1] = packet.htype
	resp[2] = packet.hlen
	resp[3] = packet.hops
	copy(resp[4:8], packet.xid[:])
	copy(resp[8:10], packet.secs[:])
	copy(resp[10:12], packet.flags[:])
	copy(resp[12:16], packet.ciaddr[:])
	if leaseIP != nil {
		copy(resp[16:20], leaseIP.To4())
	}
	serverIP := net.ParseIP(s.cfg.BootDHCPServerIP).To4()
	if serverIP != nil {
		copy(resp[20:24], serverIP)
	}
	copy(resp[28:44], packet.chaddr[:])
	resp[236], resp[237], resp[238], resp[239] = 99, 130, 83, 99

	options := dhcpOptions{}
	options.add(53, []byte{messageType})
	if serverIP != nil {
		options.add(54, serverIP)
		options.add(66, []byte(serverIP.String()))
	}
	options.add(60, []byte("PXEClient"))
	options.add(67, []byte(s.bootfile(packet)))
	if strings.ToLower(strings.TrimSpace(s.cfg.BootServiceMode)) == "builtin" {
		options.addU32(51, 3600)
		if network, ok := s.deploymentNetwork(); ok {
			if mask := subnetMask(network.CIDR); mask != nil {
				options.add(1, mask)
			}
			if router := net.ParseIP(strings.TrimSpace(network.Gateway)).To4(); router != nil {
				options.add(3, router)
			}
			if dnsBytes := dnsOptionBytes(network.DNS); len(dnsBytes) > 0 {
				options.add(6, dnsBytes)
			}
		}
	}
	return append(resp, options.bytes()...)
}

func (s PXEService) leaseIP(packet dhcpPacket) net.IP {
	start := net.ParseIP(strings.TrimSpace(s.cfg.BootDHCPLeaseStart)).To4()
	end := net.ParseIP(strings.TrimSpace(s.cfg.BootDHCPLeaseEnd)).To4()
	if start == nil || end == nil {
		return nil
	}
	startN := ipv4ToUint32(start)
	endN := ipv4ToUint32(end)
	if startN > endN {
		return nil
	}
	h := fnv.New32a()
	_, _ = h.Write(packet.chaddr[:packet.hlen])
	offset := h.Sum32() % (endN - startN + 1)
	return uint32ToIPv4(startN + offset)
}

func (s PXEService) bootfile(packet dhcpPacket) string {
	arch := packet.optionUint16(93)
	switch arch {
	case 7, 9, 11:
		return strings.TrimSpace(s.cfg.BootTFTPBootfileUEFI)
	default:
		return strings.TrimSpace(s.cfg.BootTFTPBootfileBIOS)
	}
}

func (s PXEService) deploymentNetwork() (models.NetworkConfig, bool) {
	var network models.NetworkConfig
	if err := s.db.Where("purpose = ? AND status = ?", "deployment", "enabled").Order("updated_at desc, id desc").First(&network).Error; err != nil {
		return models.NetworkConfig{}, false
	}
	return network, true
}

func (s PXEService) startTFTP(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp4", s.cfg.BootTFTPListenAddr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("listen TFTP on %s: %w", s.cfg.BootTFTPListenAddr, err)
	}
	s.logger("TFTP listener active on %s root=%s", s.cfg.BootTFTPListenAddr, s.cfg.BootTFTPRoot)
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go s.serveTFTP(conn)
	return nil
}

func (s PXEService) serveTFTP(conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.logger("TFTP read failed: %v", err)
			}
			return
		}
		req, err := parseTFTPRRQ(buf[:n])
		if err != nil {
			continue
		}
		go s.serveTFTPRequest(addr, req)
	}
}

func (s PXEService) serveTFTPRequest(addr *net.UDPAddr, req tftpRRQ) {
	data, err := s.tftpData(req.filename)
	if err != nil {
		s.tftpError(addr, 1, err.Error())
		return
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return
	}
	defer conn.Close()
	blockSize := req.blockSize()
	if req.hasOptions() {
		if err := tftpSendOACK(conn, addr, req, len(data), blockSize); err != nil {
			return
		}
		if err := tftpWaitACK(conn, addr, 0); err != nil {
			return
		}
	}
	reader := bytes.NewReader(data)
	block := uint16(1)
	for {
		chunk := make([]byte, blockSize)
		n, readErr := io.ReadFull(reader, chunk)
		if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
			chunk = chunk[:n]
		} else if readErr != nil {
			return
		}
		if err := tftpSendDataWithRetry(conn, addr, block, chunk); err != nil {
			return
		}
		if len(chunk) < blockSize {
			return
		}
		block++
	}
}

func (s PXEService) tftpData(filename string) ([]byte, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(filename, "\\", "/"))
	if raw == "" || strings.HasPrefix(raw, "/") {
		return nil, errors.New("invalid TFTP filename")
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return nil, errors.New("invalid TFTP filename")
		}
	}
	clean := path.Clean("/" + raw)
	clean = strings.TrimPrefix(clean, "/")
	switch clean {
	case "boot.ipxe", "auto.ipxe", "default.ipxe":
		script := fmt.Sprintf("#!ipxe\nset base-url %s\nchain ${base-url}/boot/ipxe?mac=${net0/mac}&arch=${buildarch}&firmware=${platform} || shell\n", strings.TrimRight(s.cfg.BootBaseURL, "/"))
		return []byte(script), nil
	}
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return nil, errors.New("invalid TFTP filename")
	}
	rootAbs, err := filepath.Abs(s.cfg.BootTFTPRoot)
	if err != nil {
		return nil, err
	}
	fileAbs, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(clean)))
	if err != nil {
		return nil, err
	}
	if !pathWithinDir(rootAbs, fileAbs) {
		return nil, errors.New("TFTP filename escapes root")
	}
	return os.ReadFile(fileAbs)
}

func (s PXEService) tftpError(addr *net.UDPAddr, code uint16, message string) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return
	}
	defer conn.Close()
	payload := []byte{0, 5, byte(code >> 8), byte(code)}
	payload = append(payload, []byte(message)...)
	payload = append(payload, 0)
	_, _ = conn.WriteToUDP(payload, addr)
}

type dhcpPacket struct {
	htype   byte
	hlen    byte
	hops    byte
	xid     [4]byte
	secs    [2]byte
	flags   [2]byte
	ciaddr  [4]byte
	chaddr  [16]byte
	options map[byte][]byte
}

func parseDHCPPacket(data []byte) (dhcpPacket, error) {
	if len(data) < 240 || data[0] != 1 || data[236] != 99 || data[237] != 130 || data[238] != 83 || data[239] != 99 {
		return dhcpPacket{}, errors.New("not a DHCP request")
	}
	var p dhcpPacket
	p.htype = data[1]
	p.hlen = data[2]
	p.hops = data[3]
	copy(p.xid[:], data[4:8])
	copy(p.secs[:], data[8:10])
	copy(p.flags[:], data[10:12])
	copy(p.ciaddr[:], data[12:16])
	copy(p.chaddr[:], data[28:44])
	p.options = parseDHCPOptions(data[240:])
	return p, nil
}

func parseDHCPOptions(data []byte) map[byte][]byte {
	options := map[byte][]byte{}
	for i := 0; i < len(data); {
		code := data[i]
		i++
		if code == 255 {
			break
		}
		if code == 0 {
			continue
		}
		if i >= len(data) {
			break
		}
		length := int(data[i])
		i++
		if i+length > len(data) {
			break
		}
		options[code] = append([]byte{}, data[i:i+length]...)
		i += length
	}
	return options
}

func (p dhcpPacket) macString() string {
	hlen := int(p.hlen)
	if hlen <= 0 || hlen > len(p.chaddr) {
		hlen = 6
	}
	return strings.ToLower(net.HardwareAddr(p.chaddr[:hlen]).String())
}

func (p dhcpPacket) isPXEClient() bool {
	return strings.Contains(strings.ToLower(string(p.options[60])), "pxeclient")
}

func (p dhcpPacket) optionUint16(code byte) uint16 {
	value := p.options[code]
	if len(value) < 2 {
		return 0
	}
	return binary.BigEndian.Uint16(value[:2])
}

func (p dhcpPacket) architecture() string {
	switch p.optionUint16(93) {
	case 7, 9, 11:
		return "x86_64"
	default:
		return "x86"
	}
}

func (p dhcpPacket) firmware() string {
	switch p.optionUint16(93) {
	case 7, 9, 11:
		return "uefi"
	default:
		return "bios"
	}
}

type dhcpOptions struct{ buf []byte }

func (o *dhcpOptions) add(code byte, value []byte) {
	if len(value) > 255 {
		value = value[:255]
	}
	o.buf = append(o.buf, code, byte(len(value)))
	o.buf = append(o.buf, value...)
}

func (o *dhcpOptions) addU32(code byte, value uint32) {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, value)
	o.add(code, buf)
}

func (o *dhcpOptions) bytes() []byte {
	return append(o.buf, 255)
}

type tftpRRQ struct {
	filename string
	mode     string
	options  map[string]string
}

func parseTFTPRRQ(data []byte) (tftpRRQ, error) {
	if len(data) < 4 || data[0] != 0 || data[1] != 1 {
		return tftpRRQ{}, errors.New("not a TFTP RRQ")
	}
	parts := bytes.Split(data[2:], []byte{0})
	if len(parts) < 2 {
		return tftpRRQ{}, errors.New("invalid TFTP RRQ")
	}
	req := tftpRRQ{filename: string(parts[0]), mode: strings.ToLower(string(parts[1])), options: map[string]string{}}
	for i := 2; i+1 < len(parts); i += 2 {
		key := strings.ToLower(strings.TrimSpace(string(parts[i])))
		value := strings.TrimSpace(string(parts[i+1]))
		if key != "" {
			req.options[key] = value
		}
	}
	if strings.TrimSpace(req.filename) == "" {
		return tftpRRQ{}, errors.New("empty TFTP filename")
	}
	return req, nil
}

func (r tftpRRQ) hasOptions() bool { return len(r.options) > 0 }

func (r tftpRRQ) blockSize() int {
	if value, ok := r.options["blksize"]; ok {
		parsed, err := strconv.Atoi(value)
		if err == nil && parsed >= 8 && parsed <= 1468 {
			return parsed
		}
	}
	return 512
}

func tftpSendOACK(conn *net.UDPConn, addr *net.UDPAddr, req tftpRRQ, size int, blockSize int) error {
	payload := []byte{0, 6}
	if _, ok := req.options["blksize"]; ok {
		payload = appendTFTPOption(payload, "blksize", strconv.Itoa(blockSize))
	}
	if _, ok := req.options["tsize"]; ok {
		payload = appendTFTPOption(payload, "tsize", strconv.Itoa(size))
	}
	_, err := conn.WriteToUDP(payload, addr)
	return err
}

func appendTFTPOption(payload []byte, key, value string) []byte {
	payload = append(payload, []byte(key)...)
	payload = append(payload, 0)
	payload = append(payload, []byte(value)...)
	payload = append(payload, 0)
	return payload
}

func tftpSendDataWithRetry(conn *net.UDPConn, addr *net.UDPAddr, block uint16, data []byte) error {
	payload := make([]byte, 4+len(data))
	payload[1] = 3
	binary.BigEndian.PutUint16(payload[2:4], block)
	copy(payload[4:], data)
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := conn.WriteToUDP(payload, addr); err != nil {
			return err
		}
		if err := tftpWaitACK(conn, addr, block); err == nil {
			return nil
		}
	}
	return errors.New("TFTP ACK timeout")
}

func tftpWaitACK(conn *net.UDPConn, addr *net.UDPAddr, block uint16) error {
	buf := make([]byte, 516)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		if from.IP.String() != addr.IP.String() || from.Port != addr.Port {
			continue
		}
		if n >= 4 && buf[0] == 0 && buf[1] == 4 && binary.BigEndian.Uint16(buf[2:4]) == block {
			return nil
		}
		if n >= 4 && buf[0] == 0 && buf[1] == 5 {
			return errors.New("TFTP client returned error")
		}
	}
}

func subnetMask(cidr string) net.IP {
	_, ipNet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return nil
	}
	return net.IP(ipNet.Mask)
}

func dnsOptionBytes(value string) []byte {
	var out []byte
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		if ip := net.ParseIP(strings.TrimSpace(item)).To4(); ip != nil {
			out = append(out, ip...)
		}
	}
	return out
}

func ipv4ToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func uint32ToIPv4(value uint32) net.IP {
	return net.IPv4(byte(value>>24), byte(value>>16), byte(value>>8), byte(value))
}

func pathWithinDir(dir string, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel))
}
