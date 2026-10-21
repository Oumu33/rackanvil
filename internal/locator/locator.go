// Package locator drives physical disk locate LEDs ("硬盘定位/点灯").
//
// Doc basis (vendor public article "介绍一款给服务器硬盘定位(点灯)工具"):
// "无Agent物理磁盘定位：仅通过SSH协议驱动底层指令…一键点亮NVMe、RAID及
// HBA直通盘的Locate LED". Our clean-room design: the server dispatches a
// typed agent task (or SSH command); the agent executes the locate on/off
// via the first available tool in the preference chain:
//
//	ledctl (ledmon, SES/VMD aware) → sg_ses (SES enclosure) →
//	storcli (MegaRAID) → nvme (NVMe-MI, where supported)
//
// All hardware access goes through the Runner interface so tests use a mock.
package locator

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner executes a command. Production uses OSRunner; tests use MockRunner.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)
}

// OSRunner runs real OS commands.
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	var so, se strings.Builder
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	return so.String(), se.String(), err
}

// Disk is one physical disk visible for locate operations.
type Disk struct {
	Device    string `json:"device"`    // /dev/sda
	Model     string `json:"model"`     // "ST12000NM000J"
	Serial    string `json:"serial"`    //
	Enclosure string `json:"enclosure"` // SES sg device, e.g. /dev/sg2 ("" if unknown)
	Slot      int    `json:"slot"`      // enclosure slot, -1 if unknown
	// Ctrl/Enc are the numeric storcli addressing components (M7): when
	// Ctrl >= 0 && Enc >= 0 && Slot >= 0 the vendor-verified structured
	// invocation "storcli64 /c{Ctrl}/e{Enc}/s{Slot} start locate" is used
	// instead of the legacy free-form Enclosure string.
	Ctrl int    `json:"ctrl"` // controller index, -1 if unknown
	Enc  int    `json:"enc"`  // enclosure id, -1 if unknown
	Tool string `json:"tool"` // tool that will drive the LED
}

// locateResult is reported back through the agent task stdout.
type locateResult struct {
	Device string `json:"device"`
	On     bool   `json:"on"`
	Tool   string `json:"tool"`
	Note   string `json:"note,omitempty"`
}

// which checks tool availability.
func which(ctx context.Context, r Runner, name string) bool {
	_, _, err := r.Run(ctx, "sh", "-c", "command -v "+name)
	return err == nil
}

// Scan enumerates block disks from sysfsRoot ("/sys" in production) and picks
// a locate tool per disk. Enclosure/slot come from the kernel's enclosure
// sysfs links when present.
func Scan(ctx context.Context, r Runner, sysfsRoot string) ([]Disk, error) {
	out, _, err := r.Run(ctx, "lsblk", "-J", "-o", "NAME,MODEL,SERIAL,TYPE", "-e", "7,11")
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	var parsed struct {
		Blockdevices []struct {
			Name, Model, Serial, Type string
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("lsblk parse: %w", err)
	}
	var disks []Disk
	for _, b := range parsed.Blockdevices {
		if b.Type != "disk" {
			continue
		}
		d := Disk{Device: "/dev/" + b.Name, Model: strings.TrimSpace(b.Model),
			Serial: strings.TrimSpace(b.Serial), Slot: -1, Ctrl: -1, Enc: -1}
		d.Enclosure, d.Slot = findEnclosure(r, sysfsRoot, b.Name)
		d.Tool = pickTool(ctx, r, &d)
		disks = append(disks, d)
	}
	return disks, nil
}

// findEnclosure reads /sys/block/<dev>/device/enclosure_device:*/slot symlinks.
func findEnclosure(r Runner, sysfsRoot, dev string) (string, int) {
	// Kept simple and mockable: ask the runner to list; production path uses
	// real sysfs. The mock answers from its script.
	out, _, err := r.Run(context.Background(), "sh", "-c",
		fmt.Sprintf("ls -d %s/block/%s/device/enclosure_device:* 2>/dev/null", sysfsRoot, dev))
	if err != nil || strings.TrimSpace(out) == "" {
		return "", -1
	}
	enc := strings.TrimSpace(strings.Split(out, "\n")[0])
	// Derive sg device: enclosure sysfs dir name maps to /dev/sg<N> via
	// "device" link target in real kernels; here we just report the path.
	return enc, 0
}

// pickTool chooses the locate driver. NVMe prefers ledctl (VMD aware),
// then nvme; SAS/SATA prefer ledctl, then sg_ses, then storcli.
func pickTool(ctx context.Context, r Runner, d *Disk) string {
	isNVMe := strings.HasPrefix(d.Device, "/dev/nvme")
	has := func(n string) bool { return which(ctx, r, n) }
	switch {
	case has("ledctl"):
		return "ledctl"
	case isNVMe && has("nvme"):
		return "nvme"
	case d.Enclosure != "" && has("sg_ses"):
		return "sg_ses"
	case has("storcli") || has("storcli64"):
		return "storcli"
	default:
		return "none"
	}
}

// Locate turns the identify LED on or off for device, returning which tool
// was used. It never guesses: unknown tool => error.
func Locate(ctx context.Context, r Runner, d Disk, on bool) (locateResult, error) {
	res := locateResult{Device: d.Device, On: on}
	switch d.Tool {
	case "ledctl":
		arg := "locate_off=/dev/" + shortDev(d.Device)
		if on {
			arg = "locate=/dev/" + shortDev(d.Device)
		}
		_, _, err := r.Run(ctx, "ledctl", arg)
		if err != nil {
			return res, fmt.Errorf("ledctl: %w", err)
		}
		res.Tool = "ledctl"
	case "sg_ses":
		v := "0"
		if on {
			v = "1"
		}
		_, _, err := r.Run(ctx, "sg_ses", "--set=ident="+v, d.Enclosure)
		if err != nil {
			return res, fmt.Errorf("sg_ses: %w", err)
		}
		res.Tool = "sg_ses"
	case "storcli":
		// Factory state (M7): the vendor's locate IS storcli/perccli —
		// the collectors binary embeds the vendor locate help
		// ("storcli /cx/ex/sx start locate", VD form "/cx/vx start
		// locate", perccli "/sasx/..." variants); the strings "ledctl"
		// and "sg_ses" appear NOWHERE in the vendor assets. Those remain
		// our own non-factory fallbacks above/below.
		bin := "storcli"
		if !which(ctx, r, bin) {
			bin = "storcli64"
		}
		action := "stop locate"
		if on {
			action = "start locate"
		}
		// Structured addressing (vendor "/cx/ex/sx" form, argv without
		// shell) when numeric components are known:
		if d.Ctrl >= 0 && d.Enc >= 0 && d.Slot >= 0 {
			target := fmt.Sprintf("/c%d/e%d/s%d", d.Ctrl, d.Enc, d.Slot)
			_, _, err := r.Run(ctx, bin, target, strings.Fields(action)[0], "locate")
			if err != nil {
				return res, fmt.Errorf("storcli: %w", err)
			}
			res.Tool = bin
			break
		}
		// Legacy path: Enclosure:slot addressing needs controller context;
		// the agent resolves it via `storcli /c0/eall/sall show` beforehand
		// and stores the resolved address in Disk.Enclosure as "cCeEsS".
		_, _, err := r.Run(ctx, "sh", "-c",
			fmt.Sprintf("%s %s %s", bin, d.Enclosure, action))
		if err != nil {
			return res, fmt.Errorf("storcli: %w", err)
		}
		res.Tool = bin
	case "nvme":
		// NVMe-MI locate via set-feature is vendor-dependent; use ledctl
		// path when available, otherwise report unsupported honestly.
		return res, fmt.Errorf("nvme locate needs ledctl/VMD on this platform")
	default:
		return res, fmt.Errorf("no locate tool available for %s", d.Device)
	}
	return res, nil
}

func shortDev(dev string) string {
	return strings.TrimPrefix(dev, "/dev/")
}

// ScanJSON is the agent task handler payload contract: stdout carries JSON.
func ScanJSON(ctx context.Context, r Runner) (string, error) {
	disks, err := Scan(ctx, r, "/sys")
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(disks)
	return string(b), err
}

// LocateJSON handles {"device":..., "enclosure":..., "tool":..., "on":bool}
// plus optional M7 numeric addressing {"ctrl":0, "enc":8, "slot":0}.
func LocateJSON(ctx context.Context, r Runner, payload string) (string, error) {
	var p struct {
		Device, Enclosure, Tool string
		Ctrl, Enc, Slot         *int
		On                      bool
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", err
	}
	d := Disk{Device: p.Device, Enclosure: p.Enclosure, Tool: p.Tool, Slot: -1, Ctrl: -1, Enc: -1}
	if p.Slot != nil {
		d.Slot = *p.Slot
	}
	if p.Ctrl != nil {
		d.Ctrl = *p.Ctrl
	}
	if p.Enc != nil {
		d.Enc = *p.Enc
	}
	res, err := Locate(ctx, r, d, p.On)
	if err != nil {
		return "", err
	}
	b, merr := json.Marshal(res)
	return string(b), merr
}

// DefaultTimeout bounds a locate operation.
const DefaultTimeout = 30 * time.Second
