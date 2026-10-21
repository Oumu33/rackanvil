package orchestrator

// M7 BMC template tests: lock the vendor-aligned Redfish paths and IPMI
// command templates (data recovered from the vendor server binary, see
// bmc.go provenance).

import (
	"strings"
	"testing"
)

func TestRedfishTemplates(t *testing.T) {
	p := RedfishPower("dell", "GracefulRestart")
	if p.Method != "POST" || p.Path != "/redfish/v1/Systems/System.Embedded.1/Actions/ComputerSystem.Reset" {
		t.Errorf("dell power = %+v", p)
	}
	if p.Body != `{"ResetType":"GracefulRestart"}` {
		t.Errorf("body = %s", p.Body)
	}
	p = RedfishPower("generic", "On")
	if p.Path != "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset" {
		t.Errorf("generic power = %+v", p)
	}
	b := RedfishBootPXE("hpe")
	if b.Method != "PATCH" || b.Path != "/redfish/v1/Systems/1" ||
		!strings.Contains(b.Body, `"BootSourceOverrideTarget":"Pxe"`) ||
		!strings.Contains(b.Body, `"BootSourceOverrideEnabled":"Once"`) ||
		!strings.Contains(b.Body, `"BootSourceOverrideMode":"UEFI"`) {
		t.Errorf("boot pxe = %+v", b)
	}
	if SystemIDFor("openbmc") != "system" {
		t.Errorf("openbmc id = %q", SystemIDFor("openbmc"))
	}
	u := RedfishStartUpdate()
	if u.Path != "/redfish/v1/UpdateService/Actions/UpdateService.StartUpdate" {
		t.Errorf("update = %+v", u)
	}
}

func TestIPMITemplates(t *testing.T) {
	if IPMIBootPXE != "/usr/bin/ipmitool chassis bootdev pxe" {
		t.Errorf("boot pxe = %q", IPMIBootPXE)
	}
	if IPMIPower("cycle") != "ipmitool chassis power cycle" {
		t.Errorf("power = %q", IPMIPower("cycle"))
	}
	cmds := IPMIProvisionUser("rackanvil")
	joined := strings.Join(cmds, "\n")
	for _, verb := range []string{"user list 1", "user set name 2 rackanvil", "user set password 2", "channel setaccess 1", "channel setaccess 8", "user enable 2", "lan print", "chassis bootparam get 5"} {
		if !strings.Contains(joined, verb) {
			t.Errorf("provision missing %q: %v", verb, cmds)
		}
	}
}
