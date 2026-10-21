// Command server is the rackanvil management server entrypoint.
//
// M1: HTTP API on :9100 (agent protocol + iPXE bootscript) and an optional
// DHCP/PXE service on :67 (requires root).
// M2: database persistence, collectors, web console.
// M3: orchestration, discovery, license, preseed install, dashboard, WebSSH.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Oumu33/rackanvil/internal/api"
	"github.com/Oumu33/rackanvil/internal/auth"
	"github.com/Oumu33/rackanvil/internal/db"
	"github.com/Oumu33/rackanvil/internal/license"
	"github.com/Oumu33/rackanvil/internal/orchestrator"
	"github.com/Oumu33/rackanvil/internal/pxe"
	"github.com/Oumu33/rackanvil/internal/version"
	"github.com/Oumu33/rackanvil/internal/web"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	addr := flag.String("addr", ":9100", "HTTP listen address")
	publicURL := flag.String("public-url", "http://127.0.0.1:9100", "externally reachable base URL (used in bootscript)")
	dbDSN := flag.String("db", "sqlite:./rackanvil.db", "database DSN: sqlite:/path/to.db or postgres://... (production)")
	assets := flag.String("assets", "./assets", "boot assets directory (./assets/debian-bookworm/{vmlinuz,initrd.img})")
	preseedDir := flag.String("preseed-dir", "./assets/preseed", "preseed templates directory")
	agentBin := flag.String("agent-bin", "", "agent binary path served at /agent/download (installer late_command)")
	licensePubKey := flag.String("license-pubkey", "", "path to Ed25519 public key file (base64url); enables license enforcement")
	licenseFile := flag.String("license", "", "path to license file to verify at boot")
	mirrorHost := flag.String("mirror-host", "", "Debian mirror host for preseed (default deb.debian.org); e.g. 10.0.0.5:8080 for a local mirror")
	mirrorDir := flag.String("mirror-dir", "", "Debian mirror directory (default /debian)")
	enableDHCP := flag.Bool("dhcp", false, "enable DHCP/PXE service on :67 (needs root)")
	logRequests := flag.Bool("log-requests", false, "log every HTTP request with raw path + status (boot-chain forensics)")
	// M4: ProxyDHCP mode — answer PXE boot info only, don't touch the
	// existing DHCP server's address allocation (vendor doc: 不动 DHCP，从旁路切入).
	dhcpProxy := flag.Bool("dhcp-proxy", false, "ProxyDHCP mode: boot info only, no leases")
	dhcpServerIP := flag.String("dhcp-server-ip", "192.168.100.1", "DHCP server identifier IP")
	dhcpPoolStart := flag.String("dhcp-pool-start", "192.168.100.100", "DHCP pool start")
	dhcpPoolEnd := flag.String("dhcp-pool-end", "192.168.100.200", "DHCP pool end")
	flag.Parse()
	if *showVersion {
		fmt.Println("rackanvil-server " + version.String())
		return
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)
	logger.Printf("server: %s", version.String())
	database, err := db.Open(*dbDSN)
	if err != nil {
		logger.Fatalf("db: %v", err)
	}
	defer database.Close()
	logger.Printf("db: connected (%s)", *dbDSN)
	srv := api.New(database, database, *publicURL, logger)
	srv.SetRequestLog(*logRequests)

	// M3: license boot check (our own Ed25519 scheme).
	var licPub ed25519.PublicKey
	if *licensePubKey != "" {
		raw, err := os.ReadFile(*licensePubKey)
		if err != nil {
			logger.Fatalf("license: read pubkey: %v", err)
		}
		licPub, err = license.ParsePubKey(string(raw))
		if err != nil {
			logger.Fatalf("license: parse pubkey: %v", err)
		}
		if *licenseFile != "" {
			lraw, err := os.ReadFile(*licenseFile)
			if err != nil {
				logger.Fatalf("license: read license: %v", err)
			}
			lic, err := license.Verify(licPub, string(lraw))
			if err != nil {
				logger.Fatalf("license: INVALID: %v", err)
			}
			logger.Printf("license: OK customer=%q max_hosts=%d expires=%s",
				lic.Customer, lic.MaxHosts, time.Unix(lic.ExpiresAt, 0).Format("2006-01-02"))
		} else {
			logger.Printf("license: pubkey configured, no license file supplied (unlicensed mode)")
		}
	} else {
		logger.Printf("license: dev mode (no --license-pubkey)")
	}

	// M3: attach backends.
	srv.AttachM3(api.M3Deps{
		Jobs: database, Install: database, Discovery: database, Licenses: database,
		LicensePub: licPub, AgentBinPath: *agentBin, PreseedDir: *preseedDir,
		MirrorHost: *mirrorHost, MirrorDir: *mirrorDir,
	})
	logger.Printf("m3: orchestrator/discovery/license/preseed backends attached")

	// M3: orchestration runner (background).
	runner := orchestrator.New(database, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.Run(ctx)

	// Web console (M2): JWT secret persisted in system_settings so sessions
	// survive restarts; generated once, never hardcoded.
	secretHex, ok := database.GetSetting("jwt_secret")
	if !ok {
		var raw [32]byte
		rand.Read(raw[:])
		secretHex = hex.EncodeToString(raw[:])
		if err := database.SetSetting("jwt_secret", secretHex); err != nil {
			logger.Fatalf("db: persist jwt secret: %v", err)
		}
	}
	secret, err := hex.DecodeString(secretHex)
	if err != nil {
		logger.Fatalf("db: bad jwt secret: %v", err)
	}
	console := web.New(database, database, auth.NewSigner(secret), logger)
	console.EnsureAdmin() // first boot: random admin password printed to log
	console.RegisterM3(licPub)
	console.RegisterM4() // M4: locator/topology/firmware/raid/cfgarchive/relay
	srv.Mount("/", console.Handler())
	logger.Printf("web: console mounted at /")
	srv.SetAssetDir(*assets)
	logger.Printf("assets: %s", *assets)

	if *enableDHCP {
		dcfg := pxe.Config{
			ServerIP:       net.ParseIP(*dhcpServerIP),
			Netmask:        net.CIDRMask(24, 32),
			Router:         net.ParseIP(*dhcpServerIP),
			DNS:            []net.IP{net.ParseIP("223.5.5.5")},
			PoolStart:      net.ParseIP(*dhcpPoolStart),
			PoolEnd:        net.ParseIP(*dhcpPoolEnd),
			LeaseTime:      1 * time.Hour,
			NextServerIP:   net.ParseIP(*dhcpServerIP),
			BootFileBIOS:   "undionly.kpxe",
			BootFileUEFI:   "ipxe.efi",
			BootFileARM64:  "bootaa64.efi", // M4: option 93 0x000B
			Proxy:          *dhcpProxy,
			BootScriptBase: *publicURL,
			Logger:         logger,
		}
		dhcp := pxe.NewServer(dcfg)
		stop := make(chan struct{})
		go func() {
			if err := dhcp.Serve(":67", stop); err != nil {
				logger.Fatalf("dhcp: %v", err)
			}
		}()
		logger.Printf("dhcp: enabled")
		defer close(stop)
	}

	logger.Printf("server: listening on %s (public URL %s)", *addr, *publicURL)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		logger.Fatalf("http: %v", err)
	}
}
