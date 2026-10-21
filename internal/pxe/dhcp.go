// Package pxe implements the rackanvil DHCP/PXE provisioning service.
//
// It is a small, self-contained DHCP server (RFC 2131) written from scratch
// for bare-metal provisioning: it answers DISCOVER/OFFER and REQUEST/ACK and
// selects the boot file based on the client's architecture (option 93) and
// vendor class (option 60), so BIOS, UEFI and iPXE clients each get the right
// next step. iPXE clients are chained directly to this server's per-host
// bootscript over HTTP.
package pxe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DHCP option codes we use.
const (
	optPad          = 0
	optSubnetMask   = 1
	optRouter       = 3
	optDNSServer    = 6
	optHostname     = 12
	optRequestedIP  = 50
	optLeaseTime    = 51
	optMessageType  = 53
	optServerID     = 54
	optVendorClass  = 60
	optUserClass    = 77
	optClientArch   = 93
	optClientGUID   = 97
	optIPXEFeatures = 175 // iPXE-private option; only iPXE ever sends it
	optEnd          = 255
)

const (
	msgDiscover = 1
	msgOffer    = 2
	msgRequest  = 3
	msgDecline  = 4
	msgAck      = 5
	msgNak      = 6
)

var dhcpMagic = []byte{99, 130, 83, 99}

// Config tunes the DHCP service.
type Config struct {
	ServerIP     net.IP        // this server's IP (option 54)
	Netmask      net.IPMask    // option 1
	Router       net.IP        // option 3
	DNS          []net.IP      // option 6
	PoolStart    net.IP        // first leasable IP
	PoolEnd      net.IP        // last leasable IP
	LeaseTime    time.Duration // option 51
	NextServerIP net.IP        // siaddr: TFTP server for BIOS/UEFI boot files
	BootFileBIOS string        // e.g. "undionly.kpxe" (served via TFTP in M2)
	BootFileUEFI string        // e.g. "ipxe.efi"
	// BootFileARM64 is the UEFI boot file for ARM64 (DHCP option 93 = 0x000B),
	// e.g. "bootaa64.efi". Doc basis: vendor article "信创异构服务器批量PXE部署":
	// "0x0007/0x0009 → x86_64 UEFI, 0x000B → ARM64 UEFI".
	BootFileARM64 string
	// Proxy enables ProxyDHCP mode (RFC 2131 / PXE spec): the server answers
	// DISCOVER with an OFFER that carries ONLY PXE boot info (siaddr +
	// bootfile-name), assigning no IP (yiaddr = 0.0.0.0). The client's real
	// DHCP server keeps doing address allocation untouched.
	// Doc basis: vendor article "不动 DHCP，从旁路切入".
	Proxy bool
	// BootScriptBase is the HTTP base of this server, e.g.
	// "http://192.168.1.10:9100". iPXE clients get chained straight to
	// <base>/api/v1/ipxe/bootscript?mac=....
	BootScriptBase string
	Logger         *log.Logger
}

// lease is a single address assignment.
type lease struct {
	ip      net.IP
	expires time.Time
}

// Server is a DHCP server with an in-memory lease table.
type Server struct {
	cfg    Config
	mu     sync.Mutex
	leases map[string]*lease // client MAC string -> lease
	used   map[string]string // ip string -> client MAC string
	log    *log.Logger
}

func NewServer(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Server{
		cfg:    cfg,
		leases: make(map[string]*lease),
		used:   make(map[string]string),
		log:    cfg.Logger,
	}
}

// packet is a decoded DHCP message (only fields we need).
type packet struct {
	op      byte
	xid     uint32
	flags   uint16
	ciaddr  net.IP
	chaddr  net.HardwareAddr
	options map[byte][]byte
	rawFile [128]byte
}

func parsePacket(b []byte) (*packet, error) {
	if len(b) < 240 {
		return nil, errors.New("dhcp: packet too short")
	}
	p := &packet{op: b[0], options: make(map[byte][]byte)}
	if p.op != 1 {
		return nil, fmt.Errorf("dhcp: not a client message (op=%d)", p.op)
	}
	p.xid = binary.BigEndian.Uint32(b[4:8])
	p.flags = binary.BigEndian.Uint16(b[10:12])
	p.ciaddr = net.IP(b[12:16])
	hlen := int(b[2])
	if hlen > 16 {
		hlen = 16
	}
	p.chaddr = net.HardwareAddr(append([]byte(nil), b[28:28+hlen]...))
	copy(p.rawFile[:], b[108:236])

	opts := b[236:]
	if len(opts) < 4 || string(opts[:4]) != string(dhcpMagic) {
		return nil, errors.New("dhcp: bad magic cookie")
	}
	for i := 4; i < len(opts); {
		code := opts[i]
		i++
		if code == optPad {
			continue
		}
		if code == optEnd {
			break
		}
		if i >= len(opts) {
			break
		}
		l := int(opts[i])
		i++
		if i+l > len(opts) {
			break
		}
		p.options[code] = append([]byte(nil), opts[i:i+l]...)
		i += l
	}
	return p, nil
}

func (s *Server) allocate(mac string) net.IP {
	now := time.Now()
	if l, ok := s.leases[mac]; ok && now.Before(l.expires) {
		return l.ip
	}
	// Reclaim expired leases.
	for m, l := range s.leases {
		if now.After(l.expires) {
			delete(s.used, l.ip.String())
			delete(s.leases, m)
		}
	}
	ip := append(net.IP(nil), s.cfg.PoolStart.To4()...)
	end := s.cfg.PoolEnd.To4()
	for {
		if _, taken := s.used[ip.String()]; !taken {
			l := &lease{ip: append(net.IP(nil), ip...), expires: now.Add(s.cfg.LeaseTime)}
			s.leases[mac] = l
			s.used[ip.String()] = mac
			return l.ip
		}
		// increment
		for i := len(ip) - 1; i >= 0; i-- {
			ip[i]++
			if ip[i] != 0 {
				break
			}
		}
		if ip.Equal(end) && s.used[ip.String()] != "" {
			return nil // pool exhausted
		}
		if !ipInRange(ip, s.cfg.PoolStart, end) {
			return nil
		}
	}
}

func ipInRange(ip, start, end net.IP) bool {
	a, b, c := ip.To4(), start.To4(), end.To4()
	if a == nil || b == nil || c == nil {
		return false
	}
	for i := 0; i < 4; i++ {
		if a[i] < b[i] || a[i] > c[i] {
			return false
		}
	}
	return true
}

// isIPXEClient reports whether the client is iPXE (as opposed to a
// firmware PXE ROM). Detection is deliberately redundant, because ROM-stage
// iPXE builds vary in what they put in option 60 (M6 live capture: some
// e1000 ROM builds do not send the literal string "iPXE" there):
//
//   - option 175 present: only iPXE ever sends its private feature option.
//   - option 77 (user class) lists "iPXE": iPXE sets this by default.
//   - option 60 contains "ipxe" (case-insensitive): the classic check.
func isIPXEClient(p *packet) bool {
	if _, ok := p.options[optIPXEFeatures]; ok {
		return true
	}
	if uc, ok := p.options[optUserClass]; ok &&
		strings.Contains(strings.ToLower(string(uc)), "ipxe") {
		return true
	}
	if vc, ok := p.options[optVendorClass]; ok &&
		strings.Contains(strings.ToLower(string(vc)), "ipxe") {
		return true
	}
	return false
}

// selectBootFile picks the option-67 boot file (and whether siaddr/TFTP
// applies) based on the client identity (see isIPXEClient) and client
// architecture (opt 93).
//
//   - iPXE: chain straight to our HTTP bootscript.
//   - arch 0 (x86 BIOS): undionly.kpxe via TFTP.
//   - arch 7/9/16 (x86_64 UEFI): ipxe.efi via TFTP.
//   - arch 11 (0x000B, ARM64 UEFI): bootaa64.efi via TFTP.
//     (Doc basis: vendor 信创 article — option 93 arch values.)
func (s *Server) selectBootFile(p *packet) (bootFile string, useTFTP bool) {
	if isIPXEClient(p) {
		mac := p.chaddr.String()
		base := strings.TrimSuffix(s.cfg.BootScriptBase, "/")
		return base + "/api/v1/ipxe/bootscript?mac=" + mac, false
	}
	arch := 0
	if a, ok := p.options[optClientArch]; ok && len(a) >= 2 {
		arch = int(binary.BigEndian.Uint16(a))
	}
	switch arch {
	case 11:
		if s.cfg.BootFileARM64 != "" {
			return s.cfg.BootFileARM64, true
		}
		return s.cfg.BootFileUEFI, true
	case 7, 9, 16:
		return s.cfg.BootFileUEFI, true
	default:
		return s.cfg.BootFileBIOS, true
	}
}

func buildReply(req *packet, msgType byte, yiaddr, serverIP net.IP, cfg Config, bootFile string, useTFTP bool) []byte {
	// 548 = 576 (RFC 2131 safe max) minus IP/UDP headers; plenty for options.
	b := make([]byte, 548)
	b[0] = 2 // BOOTREPLY
	b[1] = 1 // ethernet
	b[2] = 6
	b[3] = 0
	binary.BigEndian.PutUint32(b[4:8], req.xid)
	binary.BigEndian.PutUint16(b[10:12], req.flags)
	copy(b[12:16], req.ciaddr.To4())
	copy(b[16:20], yiaddr.To4())
	if useTFTP {
		copy(b[20:24], cfg.NextServerIP.To4())
	}
	copy(b[28:44], req.chaddr)
	// The BOOTP "file" field (bytes 108..236). Classic PXE ROMs read the
	// boot file from here and ignore option 67, so both are always set.
	if bootFile != "" {
		copy(b[108:236], bootFile)
	}
	copy(b[236:240], dhcpMagic)
	o := b[240:]
	put := func(code byte, data []byte) {
		if len(o) < 2+len(data) {
			return // option space exhausted; truncate gracefully
		}
		o[0] = code
		o[1] = byte(len(data))
		copy(o[2:], data)
		o = o[2+len(data):]
	}
	put(optMessageType, []byte{msgType})
	put(optServerID, cfg.ServerIP.To4())
	lt := make([]byte, 4)
	binary.BigEndian.PutUint32(lt, uint32(cfg.LeaseTime/time.Second))
	put(optLeaseTime, lt)
	put(optSubnetMask, net.IP(cfg.Netmask).To4())
	if cfg.Router != nil {
		put(optRouter, cfg.Router.To4())
	}
	if len(cfg.DNS) > 0 {
		var dd []byte
		for _, d := range cfg.DNS {
			dd = append(dd, d.To4()...)
		}
		put(optDNSServer, dd)
	}
	if bootFile != "" {
		put(67, []byte(bootFile)) // bootfile-name
	}
	o[0] = optEnd
	return b[:548-(len(o)-1)]
}

func msgTypeName(t byte) string {
	switch t {
	case msgDiscover:
		return "DISCOVER"
	case msgRequest:
		return "REQUEST"
	default:
		return fmt.Sprintf("type=%d", t)
	}
}

// handlePacket processes one client message and returns the reply bytes plus
// where to send them. It is pure logic (no sockets) so tests can drive it.
func (s *Server) handlePacket(raw []byte) (reply []byte, to *net.UDPAddr, err error) {
	req, err := parsePacket(raw)
	if err != nil {
		return nil, nil, err
	}
	mt, ok := req.options[optMessageType]
	if !ok || len(mt) == 0 {
		return nil, nil, errors.New("dhcp: missing message type")
	}
	mac := req.chaddr.String()
	// Full client fingerprint in the log: M6 forensics on ROM-stage clients
	// needs the raw option 60/77 values, not just our interpretation.
	arch := -1
	if a, ok := req.options[optClientArch]; ok && len(a) >= 2 {
		arch = int(binary.BigEndian.Uint16(a))
	}
	_, has175 := req.options[optIPXEFeatures]
	s.log.Printf("dhcp: %s mac=%s xid=%x arch=%d opt60=%q opt77=%q opt175=%v ipxe=%v",
		msgTypeName(mt[0]), mac, req.xid, arch,
		string(req.options[optVendorClass]), string(req.options[optUserClass]),
		has175, isIPXEClient(req))

	s.mu.Lock()
	defer s.mu.Unlock()

	switch mt[0] {
	case msgDiscover:
		bootFile, useTFTP := s.selectBootFile(req)
		if s.cfg.Proxy {
			// ProxyDHCP: boot info only, no address assignment.
			s.log.Printf("dhcp: PROXY OFFER %s bootfile=%q", mac, bootFile)
			return buildReply(req, msgOffer, net.IPv4zero, s.cfg.ServerIP, s.cfg, bootFile, useTFTP), replyAddr(req), nil
		}
		ip := s.allocate(mac)
		if ip == nil {
			return nil, nil, errors.New("dhcp: pool exhausted")
		}
		s.log.Printf("dhcp: OFFER %s -> %s bootfile=%q", mac, ip, bootFile)
		return buildReply(req, msgOffer, ip, s.cfg.ServerIP, s.cfg, bootFile, useTFTP), replyAddr(req), nil

	case msgRequest:
		sid, hasSID := req.options[optServerID]
		if hasSID && !net.IP(sid).Equal(s.cfg.ServerIP) {
			return nil, nil, nil // not for us; stay silent
		}
		var want net.IP
		if rip, ok := req.options[optRequestedIP]; ok && len(rip) == 4 {
			want = net.IP(rip)
		} else if len(req.ciaddr) == 4 && !req.ciaddr.IsUnspecified() {
			want = req.ciaddr
		}
		l, ok := s.leases[mac]
		if !ok || (want != nil && !l.ip.Equal(want)) {
			// Client wants something we didn't offer: NAK so it restarts.
			s.log.Printf("dhcp: NAK %s (wanted %s)", mac, want)
			return buildReply(req, msgNak, net.IPv4zero, s.cfg.ServerIP, s.cfg, "", false), replyAddr(req), nil
		}
		l.expires = time.Now().Add(s.cfg.LeaseTime)
		bootFile, useTFTP := s.selectBootFile(req)
		s.log.Printf("dhcp: ACK %s -> %s bootfile=%q", mac, l.ip, bootFile)
		return buildReply(req, msgAck, l.ip, s.cfg.ServerIP, s.cfg, bootFile, useTFTP), replyAddr(req), nil

	default:
		return nil, nil, fmt.Errorf("dhcp: unsupported message type %d", mt[0])
	}
}

// replyAddr decides unicast vs broadcast for the reply.
//
// A client that does not own an address yet (ciaddr zero — the DISCOVER and
// selecting-REQUEST states) must be answered by broadcast. Unicasting to
// yiaddr looks RFC-legal but silently fails in practice: the client has not
// bound the address, does not answer ARP for it, and a normal kernel UDP
// send then never leaves the server at all. M6 packet capture proved this
// exact failure (4 DISCOVERs decoded, 0 reply frames on the wire, no send
// errors). Broadcast replies are also what ISC dhcpd/Windows DHCP do in
// this state, and RFC 2131 §4.1 sanctions them whenever ciaddr is zero.
// Only a client renewing with a bound ciaddr gets a unicast reply.
func replyAddr(req *packet) *net.UDPAddr {
	if len(req.ciaddr) == 4 && !req.ciaddr.IsUnspecified() {
		return &net.UDPAddr{IP: req.ciaddr, Port: 68}
	}
	return &net.UDPAddr{IP: net.IPv4bcast, Port: 68}
}

// listenDHCP opens the server socket with SO_BROADCAST (and SO_REUSEADDR)
// set, so ALL replies — broadcast and unicast — can be written from this one
// socket. That matters: replies then always leave from UDP source port 67.
// M6 live forensics showed the previous design (replies via an ephemeral
// DialUDP socket / a port-0 broadcast socket) is a prime suspect for ROM
// clients silently discarding our OFFERs: boot ROMs may filter on the
// server's source port even though RFC 2131 does not require it.
func listenDHCP(laddr string) (net.PacketConn, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var serr error
			err := c.Control(func(fd uintptr) {
				if e := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); e != nil {
					serr = e
					return
				}
				serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			})
			if err != nil {
				return err
			}
			return serr
		},
	}
	return lc.ListenPacket(context.Background(), "udp4", laddr)
}

func htons16(v uint16) uint16 { return v<<8 | v>>8 }

func ipChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// buildUDPBroadcastFrame assembles a full Ethernet frame carrying payload
// as a UDP datagram (server port 67 -> client port 68) addressed to the
// limited broadcast IP and MAC. Split out from the send so it is unit
// testable without privileges.
func buildUDPBroadcastFrame(ifaceMAC net.HardwareAddr, srcIP net.IP, payload []byte) []byte {
	frame := make([]byte, 14+20+8+len(payload))
	// Ethernet
	copy(frame[0:6], net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(frame[6:12], ifaceMAC)
	binary.BigEndian.PutUint16(frame[12:14], 0x0800)
	// IPv4
	ip := frame[14:34]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(20+8+len(payload)))
	ip[8] = 64 // TTL
	ip[9] = 17 // UDP
	copy(ip[12:16], srcIP.To4())
	copy(ip[16:20], net.IPv4bcast.To4())
	binary.BigEndian.PutUint16(ip[10:12], ipChecksum(ip[:20]))
	// UDP
	udp := frame[34:42]
	binary.BigEndian.PutUint16(udp[0:2], 67)
	binary.BigEndian.PutUint16(udp[2:4], 68)
	binary.BigEndian.PutUint16(udp[4:6], uint16(8+len(payload)))
	copy(frame[42:], payload)
	// UDP checksum over pseudo-header + segment.
	seg := make([]byte, 0, 12+8+len(payload))
	seg = append(seg, srcIP.To4()...)
	seg = append(seg, net.IPv4bcast.To4()...)
	seg = append(seg, 0, 17, byte((8+len(payload))>>8), byte(8+len(payload)))
	seg = append(seg, frame[34:]...)
	binary.BigEndian.PutUint16(udp[6:8], ipChecksum(seg))
	return frame
}

// broadcastInterface picks the interface broadcast replies should leave
// from: the one owning the DHCP ServerIP, else the first up non-loopback
// interface with a MAC.
func (s *Server) broadcastInterface() *net.Interface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var fallback *net.Interface
	for i := range ifs {
		ifi := &ifs[i]
		if ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagUp == 0 || len(ifi.HardwareAddr) != 6 {
			continue
		}
		if fallback == nil {
			fallback = ifi
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(s.cfg.ServerIP) {
				return ifi
			}
		}
	}
	return fallback
}

// rawBroadcast emits payload as a broadcast DHCP reply on the wire via an
// AF_PACKET raw socket, bypassing kernel IP routing entirely. This is how
// ISC dhcpd / dnsmasq send replies, and it matters: on hosts without a
// default route (or with the "wrong" one) a kernel sendto() to
// 255.255.255.255 fails with ENETUNREACH — observed live in M6-F round 3 —
// even though the client is one L2 hop away.
func (s *Server) rawBroadcast(payload []byte) error {
	ifi := s.broadcastInterface()
	if ifi == nil {
		return errors.New("dhcp: no usable interface for raw broadcast")
	}
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons16(0x0800)))
	if err != nil {
		return fmt.Errorf("dhcp: packet socket: %w", err)
	}
	defer syscall.Close(fd)
	frame := buildUDPBroadcastFrame(ifi.HardwareAddr, s.cfg.ServerIP, payload)
	sa := &syscall.SockaddrLinklayer{
		Protocol: htons16(0x0800),
		Ifindex:  ifi.Index,
		Halen:    6,
	}
	copy(sa.Addr[:6], net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	return syscall.Sendto(fd, frame, 0, sa)
}

// Serve runs the DHCP server on laddr (e.g. ":67") until stop is closed.
// It must run as root (or with CAP_NET_BIND_SERVICE) for port 67.
func (s *Server) Serve(laddr string, stop <-chan struct{}) error {
	pc, err := listenDHCP(laddr)
	if err != nil {
		return fmt.Errorf("dhcp: listen %s: %w", laddr, err)
	}
	defer pc.Close()
	s.log.Printf("dhcp: listening on %s", laddr)
	go func() {
		<-stop
		pc.Close()
	}()
	buf := make([]byte, 1500)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			select {
			case <-stop:
				return nil
			default:
				return err
			}
		}
		reply, to, err := s.handlePacket(append([]byte(nil), buf[:n]...))
		if err != nil {
			s.log.Printf("dhcp: %v", err)
			continue
		}
		if reply == nil {
			continue
		}
		if isBroadcast(to.IP) {
			if err := s.rawBroadcast(reply); err != nil {
				s.log.Printf("dhcp: raw broadcast: %v (falling back to kernel send)", err)
				if _, err := pc.WriteTo(reply, to); err != nil {
					s.log.Printf("dhcp: write %s: %v", to, err)
				}
			}
			continue
		}
		if _, err := pc.WriteTo(reply, to); err != nil {
			s.log.Printf("dhcp: write %s: %v", to, err)
		}
	}
}

// isBroadcast reports whether ip is the limited broadcast address.
func isBroadcast(ip net.IP) bool {
	return ip.Equal(net.IPv4bcast)
}
