package orchestrator

// BMC request templates aligned to the vendor's production implementation
// (M7, 2026-10-21). Source: the vendor server binary (go1.26.3, full
// symbols) links the gofish Redfish client library — the types
// redfish.ResetType / redfish.PowerState and
// redfish.ComputerSystem.Boot{BootSourceOverrideEnabled,
// BootSourceOverrideTarget, BootSourceOverrideMode} are all present — and
// these Redfish paths appear verbatim in its string table:
//
//	/redfish/v1/Systems/1                      (generic / HPE iLO style)
//	/redfish/v1/Systems/System.Embedded.1     (Dell iDRAC)
//	/redfish/v1/Chassis/Self                   (Supermicro style)
//	/redfish/v1/Managers/bmc                   (OpenBMC)
//	/redfish/v1/Managers/iDRAC.Embedded.1, /redfish/v1/Managers/1
//	{system}/Actions/ComputerSystem.Reset      (gofish action URL form:
//	                                           the binary contains both
//	                                           the path template "%s/Actions/ComputerSystem.Reset"
//	                                           and redfish.ResetType values)
//	/redfish/v1/UpdateService/Actions/UpdateService.StartUpdate
//	/redfish/v1/JobService/Jobs, /redfish/v1/Oem/Dell/Jobs
//	/redfish/v1/SessionService/Sessions
//
// IPMI side (from the vendor's provisioning/install scripts in the same
// binary): "/usr/bin/ipmitool chassis bootdev pxe",
// "ipmitool chassis bootparam get 5", "ipmitool lan print",
// "ipmitool user list 1", "ipmitool user set password",
// "ipmitool user set name", "ipmitool user enable",
// "ipmitool channel setaccess 1" and "... 8".
//
// These templates are DATA alignment only: our orchestrator still runs
// them in mock mode (no BMC exists in this environment). Nothing here
// claims a real BMC was driven.

import "fmt"

// SystemIDFor returns the vendor's Redfish system id per BMC family.
func SystemIDFor(family string) string {
	switch family {
	case "dell", "idrac":
		return "System.Embedded.1"
	case "openbmc":
		return "system"
	default: // generic, hpe/ilo, supermicro — the vendor's literal "/Systems/1"
		return "1"
	}
}

// RedfishRequest is one templated BMC call (not executed in mock mode).
type RedfishRequest struct {
	Method string
	Path   string
	Body   string // JSON, "" for none
}

// RedfishPower templates the gofish ComputerSystem.Reset action:
// POST {base}/redfish/v1/Systems/{id}/Actions/ComputerSystem.Reset with
// {"ResetType": "..."} (gofish redfish.ResetType values: On, ForceOff,
// GracefulShutdown, GracefulRestart, ForceRestart, Nmi, ForceOn,
// PushPowerButton, PowerCycle).
func RedfishPower(family, resetType string) RedfishRequest {
	return RedfishRequest{
		Method: "POST",
		Path:   fmt.Sprintf("/redfish/v1/Systems/%s/Actions/ComputerSystem.Reset", SystemIDFor(family)),
		Body:   fmt.Sprintf(`{"ResetType":%q}`, resetType),
	}
}

// RedfishBootPXE templates the one-time PXE boot override: PATCH the
// system's Boot object (gofish BootSourceOverride* fields).
func RedfishBootPXE(family string) RedfishRequest {
	return RedfishRequest{
		Method: "PATCH",
		Path:   fmt.Sprintf("/redfish/v1/Systems/%s", SystemIDFor(family)),
		Body:   `{"Boot":{"BootSourceOverrideEnabled":"Once","BootSourceOverrideTarget":"Pxe","BootSourceOverrideMode":"UEFI"}}`,
	}
}

// RedfishStartUpdate templates the vendor's firmware-update entry point.
func RedfishStartUpdate() RedfishRequest {
	return RedfishRequest{
		Method: "POST",
		Path:   "/redfish/v1/UpdateService/Actions/UpdateService.StartUpdate",
		Body:   `{"ImageURI":""}`,
	}
}

// IPMIBootPXE is the vendor's verbatim install-time boot-device command
// (absolute ipmitool path, as in the binary's provisioning scripts).
var IPMIBootPXE = "/usr/bin/ipmitool chassis bootdev pxe"

// IPMIPower templates an ipmitool chassis power action.
func IPMIPower(action string) string {
	return "ipmitool chassis power " + action
}

// IPMIProvisionUser templates the vendor's BMC user-provisioning command
// group (all verbs verbatim from the binary): create/rename user 2, set
// password, grant channel access on channels 1 and 8, enable.
// The password is a placeholder: real secrets never live in templates.
func IPMIProvisionUser(username string) []string {
	return []string{
		"ipmitool user list 1",
		fmt.Sprintf("ipmitool user set name 2 %s", username),
		"ipmitool user set password 2 <from-secret-store>",
		"ipmitool channel setaccess 1 2 link=on ipmi=on callin=on privilege=4",
		"ipmitool channel setaccess 8 2 link=on ipmi=on callin=on privilege=4",
		"ipmitool user enable 2",
		"ipmitool lan print",
		"ipmitool chassis bootparam get 5",
	}
}

// BMCTemplateSummary renders the aligned templates for the mock steps so
// dry-run / simulated runs show the factory-shaped requests that WOULD be
// sent, while the step stays explicitly mock-executed.
func BMCTemplateSummary(family string) string {
	p := RedfishPower(family, "GracefulRestart")
	b := RedfishBootPXE(family)
	return fmt.Sprintf("redfish %s %s %s; redfish %s %s %s; ipmi %q (templates: vendor-aligned M7, execution still mock)",
		p.Method, p.Path, p.Body, b.Method, b.Path, b.Body, IPMIBootPXE)
}
