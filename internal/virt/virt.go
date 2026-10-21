// Package virt manages virtual machines on a VMware vSphere/vCenter or
// standalone ESXi endpoint using the official govmomi (vim25) SDK.
//
// Verified against VMware govmomi v0.56.0's in-process simulator (vcsim),
// i.e. github.com/vmware/govmomi/simulator; it has NOT been run against a
// real vCenter/ESXi environment (no production hardware available).
package virt

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
)

// VM is a lightweight summary of one virtual machine.
type VM struct {
	// Name is the inventory display name of the VM.
	Name string
	// PowerState is the vSphere runtime power state, e.g. "poweredOn",
	// "poweredOff", "suspended".
	PowerState string
	// NumCPU is the configured number of virtual CPUs.
	NumCPU int32
	// MemoryMB is the configured memory size in MiB.
	MemoryMB int32
	// GuestIP is the IP reported by VMware Tools, if any. It may be empty
	// when tools are not installed or the guest has no address.
	GuestIP string
}

// Client is a connection to a vCenter Server or ESXi host.
type Client struct {
	gov *govmomi.Client
}

// NewClient connects to a vCenter/ESXi endpoint.
//
// endpoint is the SOAP endpoint URL, e.g. "https://vcenter.example.com/sdk".
// username/password are used for session login; pass an empty username to
// connect without credentials (works against the govmomi simulator).
// insecure skips TLS certificate verification (handy for lab/simulator use;
// production deployments should present a trusted CA instead).
func NewClient(ctx context.Context, endpoint, username, password string, insecure bool) (*Client, error) {
	if endpoint == "" {
		return nil, errors.New("virt: endpoint URL is required")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("virt: parse endpoint %q: %w", endpoint, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("virt: endpoint %q must use http or https", endpoint)
	}
	if username != "" {
		u.User = url.UserPassword(username, password)
	}
	gov, err := govmomi.NewClient(ctx, u, insecure)
	if err != nil {
		return nil, fmt.Errorf("virt: connect to %s: %w", u.Redacted(), err)
	}
	return &Client{gov: gov}, nil
}

// Close logs out of the session and releases idle connections.
func (c *Client) Close(ctx context.Context) error {
	if c == nil || c.gov == nil {
		return nil
	}
	return c.gov.Logout(ctx)
}

// vmProps are the VirtualMachine properties fetched by ListVMs and
// findVMRef. The paths mirror the vSphere API property names used with
// the property collector.
var vmProps = []string{
	"name",
	"runtime.powerState",
	"config.hardware.numCPU",
	"config.hardware.memoryMB",
	"guest.ipAddress",
}

// ListVMs returns every virtual machine known to the server, querying the
// property collector for name, runtime.powerState, config.hardware and
// guest.ipAddress on each VirtualMachine.
func (c *Client) ListVMs(ctx context.Context) ([]VM, error) {
	if c == nil || c.gov == nil {
		return nil, errors.New("virt: client is not connected")
	}
	m := view.NewManager(c.gov.Client)
	v, err := m.CreateContainerView(ctx, c.gov.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		return nil, fmt.Errorf("virt: create container view: %w", err)
	}
	defer func() { _ = v.Destroy(ctx) }()

	var mos []mo.VirtualMachine
	if err := v.Retrieve(ctx, []string{"VirtualMachine"}, vmProps, &mos); err != nil {
		return nil, fmt.Errorf("virt: retrieve VM properties: %w", err)
	}

	out := make([]VM, 0, len(mos))
	for _, m := range mos {
		vm := VM{Name: m.Name, PowerState: string(m.Runtime.PowerState)}
		if m.Config != nil {
			vm.NumCPU = m.Config.Hardware.NumCPU
			vm.MemoryMB = m.Config.Hardware.MemoryMB
		}
		if m.Guest != nil {
			vm.GuestIP = m.Guest.IpAddress
		}
		out = append(out, vm)
	}
	return out, nil
}

// findVM returns the object handle of the VM with the given inventory name.
func (c *Client) findVM(ctx context.Context, name string) (*object.VirtualMachine, error) {
	if name == "" {
		return nil, errors.New("virt: VM name is required")
	}
	m := view.NewManager(c.gov.Client)
	v, err := m.CreateContainerView(ctx, c.gov.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		return nil, fmt.Errorf("virt: create container view: %w", err)
	}
	defer func() { _ = v.Destroy(ctx) }()

	var mos []mo.VirtualMachine
	if err := v.Retrieve(ctx, []string{"VirtualMachine"}, []string{"name"}, &mos); err != nil {
		return nil, fmt.Errorf("virt: retrieve VM names: %w", err)
	}
	var matches []mo.VirtualMachine
	for _, m := range mos {
		if m.Name == name {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("virt: virtual machine %q not found", name)
	case 1:
		return object.NewVirtualMachine(c.gov.Client, matches[0].Reference()), nil
	default:
		return nil, fmt.Errorf("virt: multiple virtual machines named %q", name)
	}
}

// PowerOn powers on the VM with the given name and waits for the power-on
// task to complete.
func (c *Client) PowerOn(ctx context.Context, name string) error {
	vm, err := c.findVM(ctx, name)
	if err != nil {
		return err
	}
	t, err := vm.PowerOn(ctx)
	if err != nil {
		return fmt.Errorf("virt: power on %q: %w", name, err)
	}
	if err := t.Wait(ctx); err != nil {
		return fmt.Errorf("virt: wait power on %q: %w", name, err)
	}
	return nil
}

// PowerOff powers off the VM with the given name and waits for the
// power-off task to complete.
func (c *Client) PowerOff(ctx context.Context, name string) error {
	vm, err := c.findVM(ctx, name)
	if err != nil {
		return err
	}
	t, err := vm.PowerOff(ctx)
	if err != nil {
		return fmt.Errorf("virt: power off %q: %w", name, err)
	}
	if err := t.Wait(ctx); err != nil {
		return fmt.Errorf("virt: wait power off %q: %w", name, err)
	}
	return nil
}
