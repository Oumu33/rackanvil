// Package networkconfig renders host network configuration with the
// vendor's drivers (M9 switch): the vendor server ships a
// networkconfig package whose NetplanDriver.Apply renders a fixed
// text/template (283 bytes, recovered verbatim — see
// deplos-reversing/VALIDATED-DATA-CATALOG.md §1.4 and m8-evidence
// strings) to /etc/netplan/99-rackanvil-<name>.yaml and applies it with
// `netplan apply`. M8 archived the template without adopting it; M9
// adopts it as-is, keeping our variable-injection style (typed
// fields, DNS list joined with ", " as in the vendor code).
package networkconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

// vendorNetplanTemplate is the vendor template, byte-for-byte
// (escapes resolved from the recovered literal).
const vendorNetplanTemplate = `network:
  version: 2
  {{.GroupType}}:
    {{.Name}}:
      dhcp4: false
      dhcp6: false
      addresses:
        - {{.CIDR}}
      {{if .Gateway}}routes:
        - to: default
          via: {{.Gateway}}{{end}}
      {{if .DNS}}nameservers:
        addresses: [{{.DNS}}]{{end}}
`

// Config is one netplan group (a single interface or bond).
type Config struct {
	GroupType string   // "ethernets" | "bonds"
	Name      string   // interface / bond name
	CIDR      string   // address with prefix, e.g. 10.0.0.5/24
	Gateway   string   // optional default gateway
	DNS       []string // optional DNS servers
}

// Path returns the vendor's target file for this config.
func (c Config) Path() string {
	return fmt.Sprintf("/etc/netplan/99-rackanvil-%s.yaml", c.Name)
}

// Render produces the netplan YAML exactly as the vendor driver does.
func Render(c Config) (string, error) {
	if c.GroupType != "ethernets" && c.GroupType != "bonds" {
		return "", fmt.Errorf("networkconfig: group type %q (want ethernets|bonds)", c.GroupType)
	}
	if c.Name == "" || c.CIDR == "" {
		return "", fmt.Errorf("networkconfig: name and CIDR are required")
	}
	tmpl, err := template.New("netplan").Parse(vendorNetplanTemplate)
	if err != nil {
		return "", err
	}
	view := struct {
		GroupType, Name, CIDR, Gateway, DNS string
	}{c.GroupType, c.Name, c.CIDR, c.Gateway, strings.Join(c.DNS, ", ")}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, view); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Runner abstracts command execution and file writes (agent pattern).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, string, error)
	WriteFile(ctx context.Context, path, content string) error
}

// Apply renders the config, writes it to the vendor path and runs
// `netplan apply`, mirroring NetplanDriver.Apply.
func Apply(ctx context.Context, r Runner, c Config) (string, error) {
	body, err := Render(c)
	if err != nil {
		return "", err
	}
	if err := r.WriteFile(ctx, c.Path(), body); err != nil {
		return "", fmt.Errorf("networkconfig: write %s: %w", c.Path(), err)
	}
	if _, _, err := r.Run(ctx, "netplan", "apply"); err != nil {
		return "", fmt.Errorf("networkconfig: netplan apply: %w", err)
	}
	return body, nil
}

// ApplyJSON is the agent task entry point: payload is a JSON Config.
func ApplyJSON(ctx context.Context, r Runner, payload string) (string, error) {
	var c Config
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		return "", fmt.Errorf("networkconfig: bad payload: %w", err)
	}
	return Apply(ctx, r, c)
}
