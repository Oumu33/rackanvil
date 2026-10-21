package pxe

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// buildClientPacket crafts a minimal DHCPDISCOVER/DHCPREQUEST for tests.
func buildClientPacket(t *testing.T, msgType byte, mac net.HardwareAddr, opts map[byte][]byte) []byte {
	t.Helper()
	b := make([]byte, 300)
	b[0] = 1 // BOOTREQUEST
	b[1] = 1
	b[2] = 6
	binary.BigEndian.PutUint32(b[4:8], 0x12345678)
	copy(b[28:34], mac)
	copy(b[236:240], dhcpMagic)
	o := b[240:]
	put := func(code byte, data []byte) {
		o[0] = code
		o[1] = byte(len(data))
		copy(o[2:], data)
		o = o[2+len(data):]
	}
	put(optMessageType, []byte{msgType})
	for c, d := range opts {
		put(c, d)
	}
	o[0] = optEnd
	return b[:300-(len(o)-1)]
}

func testConfig() Config {
	return Config{
		ServerIP:       net.ParseIP("192.168.100.1"),
		Netmask:        net.CIDRMask(24, 32),
		Router:         net.ParseIP("192.168.100.1"),
		DNS:            []net.IP{net.ParseIP("223.5.5.5")},
		PoolStart:      net.ParseIP("192.168.100.100"),
		PoolEnd:        net.ParseIP("192.168.100.110"),
		LeaseTime:      time.Hour,
		NextServerIP:   net.ParseIP("192.168.100.1"),
		BootFileBIOS:   "undionly.kpxe",
		BootFileUEFI:   "ipxe.efi",
		BootScriptBase: "http://192.168.100.1:9100",
	}
}

func offerFor(t *testing.T, s *Server, mac string, extra map[byte][]byte) []byte {
	t.Helper()
	hw, _ := net.ParseMAC(mac)
	raw := buildClientPacket(t, msgDiscover, hw, extra)
	reply, _, err := s.handlePacket(raw)
	if err != nil {
		t.Fatalf("handlePacket: %v", err)
	}
	if reply == nil {
		t.Fatal("expected OFFER, got nil")
	}
	return reply
}

func TestDiscoverOfferBIOS(t *testing.T) {
	s := NewServer(testConfig())
	reply := offerFor(t, s, "52:54:00:12:34:56", nil)
	if reply[0] != 2 {
		t.Fatalf("op=%d, want BOOTREPLY(2)", reply[0])
	}
	yiaddr := net.IP(reply[16:20])
	if !yiaddr.Equal(net.ParseIP("192.168.100.100")) {
		t.Fatalf("yiaddr=%s, want 192.168.100.100", yiaddr)
	}
	// option 67 should be the BIOS boot file; decode options manually since
	// parsePacket() only accepts client messages (op=1).
	found := false
	for i := 240; i < len(reply)-1; {
		if reply[i] == optEnd {
			break
		}
		if reply[i] == optPad {
			i++
			continue
		}
		l := int(reply[i+1])
		if reply[i] == 67 && string(reply[i+2:i+2+l]) == "undionly.kpxe" {
			found = true
		}
		i += 2 + l
	}
	if !found {
		t.Fatal("option 67 bootfile != undionly.kpxe for BIOS client")
	}
}

func TestDiscoverOfferUEFI(t *testing.T) {
	s := NewServer(testConfig())
	arch := make([]byte, 2)
	binary.BigEndian.PutUint16(arch, 7) // x86_64 UEFI
	reply := offerFor(t, s, "52:54:00:12:34:57", map[byte][]byte{optClientArch: arch})
	found := false
	for i := 240; i < len(reply)-1; {
		if reply[i] == optEnd {
			break
		}
		if reply[i] == optPad {
			i++
			continue
		}
		l := int(reply[i+1])
		if reply[i] == 67 && string(reply[i+2:i+2+l]) == "ipxe.efi" {
			found = true
		}
		i += 2 + l
	}
	if !found {
		t.Fatal("option 67 bootfile != ipxe.efi for UEFI client")
	}
}

func TestDiscoverOfferIPXE(t *testing.T) {
	s := NewServer(testConfig())
	reply := offerFor(t, s, "52:54:00:12:34:58", map[byte][]byte{optVendorClass: []byte("iPXE")})
	want := "http://192.168.100.1:9100/api/v1/ipxe/bootscript?mac=52:54:00:12:34:58"
	found := false
	for i := 240; i < len(reply)-1; {
		if reply[i] == optEnd {
			break
		}
		if reply[i] == optPad {
			i++
			continue
		}
		l := int(reply[i+1])
		if reply[i] == 67 && string(reply[i+2:i+2+l]) == want {
			found = true
		}
		i += 2 + l
	}
	if !found {
		t.Fatalf("option 67 should chain iPXE to bootscript URL %q", want)
	}
}

func TestRequestAckAndNak(t *testing.T) {
	s := NewServer(testConfig())
	mac := "52:54:00:12:34:59"
	hw, _ := net.ParseMAC(mac)
	// DISCOVER first
	disc := buildClientPacket(t, msgDiscover, hw, nil)
	offer, _, err := s.handlePacket(disc)
	if err != nil || offer == nil {
		t.Fatalf("discover: %v", err)
	}
	offered := net.IP(offer[16:20])

	// REQUEST the offered IP -> ACK
	req := buildClientPacket(t, msgRequest, hw, map[byte][]byte{
		optRequestedIP: offered.To4(),
		optServerID:    net.ParseIP("192.168.100.1").To4(),
	})
	ack, _, err := s.handlePacket(req)
	if err != nil || ack == nil {
		t.Fatalf("request: %v", err)
	}
	if ack[242] != msgAck { // option 53 is first option after magic
		t.Fatalf("expected ACK, got option53=%d", ack[242])
	}

	// REQUEST a different IP -> NAK
	req2 := buildClientPacket(t, msgRequest, hw, map[byte][]byte{
		optRequestedIP: net.ParseIP("192.168.100.200").To4(),
		optServerID:    net.ParseIP("192.168.100.1").To4(),
	})
	nak, _, err := s.handlePacket(req2)
	if err != nil || nak == nil {
		t.Fatalf("request2: %v", err)
	}
	if nak[242] != msgNak {
		t.Fatalf("expected NAK, got option53=%d", nak[242])
	}
}

func TestPoolExhaustion(t *testing.T) {
	cfg := testConfig()
	cfg.PoolStart = net.ParseIP("192.168.100.100")
	cfg.PoolEnd = net.ParseIP("192.168.100.100") // single address
	s := NewServer(cfg)
	offerFor(t, s, "52:54:00:00:00:01", nil)
	hw, _ := net.ParseMAC("52:54:00:00:00:02")
	disc := buildClientPacket(t, msgDiscover, hw, nil)
	_, _, err := s.handlePacket(disc)
	if err == nil {
		t.Fatal("expected pool exhausted error")
	}
}

func bootfileOf(t *testing.T, reply []byte) string {
	t.Helper()
	for i := 240; i < len(reply)-1; {
		if reply[i] == optEnd {
			break
		}
		if reply[i] == optPad {
			i++
			continue
		}
		l := int(reply[i+1])
		if reply[i] == 67 {
			return string(reply[i+2 : i+2+l])
		}
		i += 2 + l
	}
	return ""
}

func TestDiscoverOfferARM64(t *testing.T) {
	cfg := testConfig()
	cfg.BootFileARM64 = "bootaa64.efi"
	s := NewServer(cfg)
	arch := make([]byte, 2)
	binary.BigEndian.PutUint16(arch, 0x000B) // ARM64 UEFI (vendor doc)
	reply := offerFor(t, s, "52:54:00:12:34:58", map[byte][]byte{optClientArch: arch})
	if got := bootfileOf(t, reply); got != "bootaa64.efi" {
		t.Fatalf("option 67 = %q, want bootaa64.efi", got)
	}
}

func TestProxyDHCPOffer(t *testing.T) {
	cfg := testConfig()
	cfg.Proxy = true
	s := NewServer(cfg)
	reply := offerFor(t, s, "52:54:00:12:34:59", nil)
	// yiaddr must be 0.0.0.0: proxy assigns no address.
	if yiaddr := net.IP(reply[16:20]); !yiaddr.Equal(net.IPv4zero) {
		t.Fatalf("proxy yiaddr=%s, want 0.0.0.0", yiaddr)
	}
	if got := bootfileOf(t, reply); got != "undionly.kpxe" {
		t.Fatalf("proxy option 67 = %q, want bootfile", got)
	}
	// Pool must remain untouched (no lease consumed).
	s.mu.Lock()
	n := len(s.leases)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("proxy consumed %d leases, want 0", n)
	}
}

// opt67 extracts option 67 (bootfile-name) from a reply packet.
func opt67(t *testing.T, reply []byte) string {
	t.Helper()
	for i := 240; i < len(reply)-1; {
		if reply[i] == optEnd {
			break
		}
		if reply[i] == optPad {
			i++
			continue
		}
		l := int(reply[i+1])
		if reply[i] == 67 {
			return string(reply[i+2 : i+2+l])
		}
		i += 2 + l
	}
	return ""
}

// M6: ROM-stage iPXE builds may not put "iPXE" in option 60 at all.
// Option 175 (iPXE private) alone must be enough to chain to bootscript.
func TestDiscoverOfferIPXEByOption175(t *testing.T) {
	s := NewServer(testConfig())
	reply := offerFor(t, s, "52:54:00:12:34:60", map[byte][]byte{
		optIPXEFeatures: {1, 1},
	})
	want := "http://192.168.100.1:9100/api/v1/ipxe/bootscript?mac=52:54:00:12:34:60"
	if got := opt67(t, reply); got != want {
		t.Fatalf("option 67 = %q, want bootscript URL %q", got, want)
	}
}

// M6: option 77 user class "iPXE" alone must also be detected.
func TestDiscoverOfferIPXEByUserClass(t *testing.T) {
	s := NewServer(testConfig())
	reply := offerFor(t, s, "52:54:00:12:34:61", map[byte][]byte{
		optUserClass: []byte("iPXE"),
	})
	want := "http://192.168.100.1:9100/api/v1/ipxe/bootscript?mac=52:54:00:12:34:61"
	if got := opt67(t, reply); got != want {
		t.Fatalf("option 67 = %q, want bootscript URL %q", got, want)
	}
}

// M6: the boot file must ALSO land in the fixed BOOTP file field — classic
// PXE ROMs read it from there and ignore option 67.
func TestOfferSetsFileField(t *testing.T) {
	s := NewServer(testConfig())
	reply := offerFor(t, s, "52:54:00:12:34:62", nil)
	file := reply[108:236]
	n := 0
	for n < len(file) && file[n] != 0 {
		n++
	}
	if string(file[:n]) != "undionly.kpxe" {
		t.Fatalf("file field = %q, want %q", file[:n], "undionly.kpxe")
	}
}

func TestIsIPXEClient(t *testing.T) {
	cases := []struct {
		name string
		opts map[byte][]byte
		want bool
	}{
		{"opt60 iPXE", map[byte][]byte{optVendorClass: []byte("iPXE")}, true},
		{"opt60 PXEClient", map[byte][]byte{optVendorClass: []byte("PXEClient:Arch:00000:UNDI:002001")}, false},
		{"opt175 only", map[byte][]byte{optIPXEFeatures: {1}}, true},
		{"opt77 iPXE", map[byte][]byte{optUserClass: []byte("iPXE")}, true},
		{"opt77 other", map[byte][]byte{optUserClass: []byte("MSFT 5.0")}, false},
		{"nothing", nil, false},
	}
	for _, c := range cases {
		p := &packet{options: c.opts}
		if p.options == nil {
			p.options = map[byte][]byte{}
		}
		if got := isIPXEClient(p); got != c.want {
			t.Errorf("%s: isIPXEClient=%v, want %v", c.name, got, c.want)
		}
	}
}

// M6: a client with no address yet must get a BROADCAST reply even when it
// did not set the broadcast flag — unicast to yiaddr dies in ARP (proven by
// packet capture: offers never reached the wire).
func TestReplyAddrBroadcastUntilBound(t *testing.T) {
	s := NewServer(testConfig())
	mac := "52:54:00:12:34:63"
	hw, _ := net.ParseMAC(mac)
	disc := buildClientPacket(t, msgDiscover, hw, nil) // flags=0, ciaddr=0
	_, to, err := s.handlePacket(disc)
	if err != nil {
		t.Fatal(err)
	}
	if !to.IP.Equal(net.IPv4bcast) {
		t.Fatalf("OFFER dest = %s, want broadcast", to.IP)
	}
	// Renewal-style REQUEST carrying a bound ciaddr -> unicast to ciaddr.
	req := buildClientPacket(t, msgRequest, hw, map[byte][]byte{optRequestedIP: net.ParseIP("192.168.100.100").To4()})
	copy(req[12:16], net.ParseIP("192.168.100.100").To4())
	_, to2, err := s.handlePacket(req)
	if err != nil {
		t.Fatal(err)
	}
	if !to2.IP.Equal(net.ParseIP("192.168.100.100")) {
		t.Fatalf("renewal ACK dest = %s, want unicast ciaddr", to2.IP)
	}
}

// M6: the raw broadcast frame must be a well-formed Ethernet/IPv4/UDP
// datagram with valid checksums (clients with checksum verification on
// would silently drop anything else).
func TestBuildUDPBroadcastFrame(t *testing.T) {
	mac, _ := net.ParseMAC("52:54:00:12:34:a1")
	payload := []byte{2, 1, 6, 0, 0xde, 0xad, 0xbe, 0xef}
	f := buildUDPBroadcastFrame(mac, net.ParseIP("192.168.100.1"), payload)
	if len(f) != 14+20+8+len(payload) {
		t.Fatalf("frame len %d", len(f))
	}
	for i := 0; i < 6; i++ {
		if f[i] != 0xff {
			t.Fatal("eth dst must be broadcast")
		}
	}
	if binary.BigEndian.Uint16(f[12:14]) != 0x0800 {
		t.Fatal("ethertype must be IPv4")
	}
	ip := f[14:34]
	if ipChecksum(ip[:20]) != 0 {
		t.Fatal("IP header checksum invalid")
	}
	if !net.IP(ip[12:16]).Equal(net.ParseIP("192.168.100.1")) || !net.IP(ip[16:20]).Equal(net.IPv4bcast) {
		t.Fatal("IP src/dst wrong")
	}
	udp := f[34:42]
	if binary.BigEndian.Uint16(udp[0:2]) != 67 || binary.BigEndian.Uint16(udp[2:4]) != 68 {
		t.Fatal("UDP ports must be 67->68")
	}
	// Verify UDP checksum: recompute over pseudo-header with zeroed field.
	seg := append([]byte{}, ip[12:20]...)
	seg = append(seg, 0, 17, udp[4], udp[5])
	seg = append(seg, f[34:]...)
	if ipChecksum(seg) != 0 {
		t.Fatal("UDP checksum invalid")
	}
	if string(f[42:]) != string(payload) {
		t.Fatal("payload mangled")
	}
}
