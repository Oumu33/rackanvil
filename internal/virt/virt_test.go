package virt_test

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/simulator"

	virt "github.com/Oumu33/rackanvil/internal/virt"
)

// newSimClient boots an in-process vcsim vCenter (simulator.VPX) with
// powered-off VMs, then connects our own virt.Client to it. Nothing in
// this test mocks or substitutes the Client under test.
func newSimClient(t *testing.T) (*virt.Client, context.Context, func()) {
	t.Helper()

	model := simulator.VPX()
	model.Autostart = false // start every VM powered off for deterministic power transitions
	model.Machine = 2       // a couple of VMs per host is enough
	if err := model.Create(); err != nil {
		t.Fatalf("simulator model create: %v", err)
	}

	srv := model.Service.NewServer()

	ctx := context.Background()
	c, err := virt.NewClient(ctx, srv.URL.String(), "", "", true)
	if err != nil {
		srv.Close()
		model.Remove()
		t.Fatalf("virt.NewClient: %v", err)
	}

	cleanup := func() {
		_ = c.Close(ctx)
		srv.Close()
		model.Remove()
	}
	return c, ctx, cleanup
}

func findByName(vms []virt.VM, name string) (virt.VM, bool) {
	for _, v := range vms {
		if v.Name == name {
			return v, true
		}
	}
	return virt.VM{}, false
}

func TestListVMsAgainstSimulator(t *testing.T) {
	c, ctx, cleanup := newSimClient(t)
	defer cleanup()

	vms, err := c.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(vms) < 2 {
		t.Fatalf("expected at least 2 VMs from the simulator, got %d", len(vms))
	}
	for _, vm := range vms {
		if vm.Name == "" {
			t.Fatalf("VM entry with empty name: %+v", vm)
		}
		switch vm.PowerState {
		case "poweredOff":
			// expected: Autostart=false
		default:
			t.Fatalf("VM %q: expected poweredOff with Autostart=false, got %q", vm.Name, vm.PowerState)
		}
		t.Logf("VM name=%s power=%s cpus=%d memMB=%d guestIP=%q",
			vm.Name, vm.PowerState, vm.NumCPU, vm.MemoryMB, vm.GuestIP)
	}
}

func TestPowerOnPowerOffAgainstSimulator(t *testing.T) {
	c, ctx, cleanup := newSimClient(t)
	defer cleanup()

	vms, err := c.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	target := vms[0].Name
	t.Logf("power transition target: %s", target)

	// PowerOn: poweredOff -> poweredOn.
	if err := c.PowerOn(ctx, target); err != nil {
		t.Fatalf("PowerOn(%q): %v", target, err)
	}
	after, err := c.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs after PowerOn: %v", err)
	}
	vm, ok := findByName(after, target)
	if !ok {
		t.Fatalf("VM %q vanished after PowerOn", target)
	}
	if vm.PowerState != "poweredOn" {
		t.Fatalf("VM %q: expected poweredOn after PowerOn, got %q", target, vm.PowerState)
	}

	// PowerOff: poweredOn -> poweredOff.
	if err := c.PowerOff(ctx, target); err != nil {
		t.Fatalf("PowerOff(%q): %v", target, err)
	}
	after, err = c.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs after PowerOff: %v", err)
	}
	vm, ok = findByName(after, target)
	if !ok {
		t.Fatalf("VM %q vanished after PowerOff", target)
	}
	if vm.PowerState != "poweredOff" {
		t.Fatalf("VM %q: expected poweredOff after PowerOff, got %q", target, vm.PowerState)
	}

	// Power ops on a nonexistent VM must fail.
	if err := c.PowerOn(ctx, "no-such-vm-12345"); err == nil {
		t.Fatal("PowerOn of nonexistent VM unexpectedly succeeded")
	}
	if err := c.PowerOff(ctx, "no-such-vm-12345"); err == nil {
		t.Fatal("PowerOff of nonexistent VM unexpectedly succeeded")
	}
}
