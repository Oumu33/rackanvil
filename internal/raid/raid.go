// Package raid wraps RAID controller CLIs for VD management.
//
// Doc basis: vendor README — "RAID 配置：自动识别阵列卡，支持 RAID0/1/5/10 配置".
// Our clean-room implementation shells out to the standard vendor tools
// (storcli/storcli64 for MegaRAID, perccli for Dell PERC, sas3ircu for LSI
// SAS HBAs). Without a real controller, all operations are verified with a
// mock runner (honestly labeled in the report); command construction follows
// the vendors' public CLI syntax.
package raid

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Runner executes commands.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)
}

// Controller is one detected RAID controller.
type Controller struct {
	ID    string `json:"id"`    // "0"
	Model string `json:"model"` // "MegaRAID SAS 9361-8i"
	Tool  string `json:"tool"`  // "storcli"
}

// VirtualDrive is one VD/Volume.
type VirtualDrive struct {
	Controller string `json:"controller"`
	ID         string `json:"id"`    // "0"
	RAID       string `json:"raid"`  // "RAID1"
	Size       string `json:"size"`  // "1.089 TB"
	State      string `json:"state"` // "Optl"
}

// PhysicalDrive is one PD.
type PhysicalDrive struct {
	Controller string `json:"controller"`
	Enclosure  string `json:"enclosure"`
	Slot       string `json:"slot"`
	Model      string `json:"model"`
	State      string `json:"state"` // "UGood"/"Onln"
}

// ShowAll is the parsed form of `storcli /cX show all J`.
//
// Real structure (verified against a live-server capture published as a test
// fixture in prometheus-community/node-exporter-textfile-collector-scripts):
//
//	{"Controllers":[{"Command Status":{"Status":"Success",...},
//	  "Response Data":{"Basics":{"Controller":0,"Model":"...","Serial Number":"..."},
//	    "Version":{"Driver Name":"megaraid_sas",...},
//	    "VD LIST":[{"DG/VD":"0/0","TYPE":"RAID1","State":"Optl","Size":"1.818 TB",...}],
//	    "PD LIST":[{"EID:Slt":"252:0","DID":"5","State":"Onln","DG":"0",
//	      "Size":"3.637 TB","Intf":"SATA","Med":"HDD","Model":"TOSHIBA HDWE140 "}]}}]}
//
// NOTE: an earlier revision of this code assumed Response Data was keyed by
// controller id with {Controller,Model} values. That structure does not exist
// in real storcli output; the assumption was replaced after fixture testing.
type ShowAll struct {
	Controllers []Controller
	VDs         []VirtualDrive
	PDs         []PhysicalDrive
}

// ParseShowAll parses `storcli show all J` output into controllers, VDs, PDs.
func ParseShowAll(data []byte) (*ShowAll, error) {
	var parsed struct {
		Controllers []struct {
			CommandStatus struct {
				Status string `json:"Status"`
			} `json:"Command Status"`
			ResponseData struct {
				Basics struct {
					Controller any    `json:"Controller"`
					Model      string `json:"Model"`
				} `json:"Basics"`
				VDList []map[string]any `json:"VD LIST"`
				PDList []map[string]any `json:"PD LIST"`
			} `json:"Response Data"`
		} `json:"Controllers"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse storcli show all: %w", err)
	}
	out := &ShowAll{}
	for _, c := range parsed.Controllers {
		if c.CommandStatus.Status != "" && c.CommandStatus.Status != "Success" {
			return nil, fmt.Errorf("storcli status: %s", c.CommandStatus.Status)
		}
		id := fmt.Sprint(c.ResponseData.Basics.Controller)
		ctrl := Controller{ID: id, Model: strings.TrimSpace(c.ResponseData.Basics.Model)}
		out.Controllers = append(out.Controllers, ctrl)
		for _, row := range c.ResponseData.VDList {
			dgvd := strField(row, "DG/VD")
			vid := dgvd
			if parts := strings.Split(dgvd, "/"); len(parts) == 2 {
				vid = parts[1]
			}
			out.VDs = append(out.VDs, VirtualDrive{
				Controller: id, ID: vid,
				RAID: strField(row, "TYPE"), Size: strField(row, "Size"), State: strField(row, "State"),
			})
		}
		for _, row := range c.ResponseData.PDList {
			eid, slot := strField(row, "EID:Slt"), ""
			if parts := strings.Split(eid, ":"); len(parts) == 2 {
				eid, slot = parts[0], parts[1]
			}
			out.PDs = append(out.PDs, PhysicalDrive{
				Controller: id, Enclosure: eid, Slot: slot,
				Model: strings.TrimSpace(strField(row, "Model")), State: strField(row, "State"),
			})
		}
	}
	return out, nil
}

// ParseStorcliShowText parses the text mode `storcli /cX show` header,
// returning the product model and serial number. Used as a fallback when
// the JSON mode is unavailable (older storcli builds).
func ParseStorcliShowText(data []byte) (model, serial string) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := cutPrefix(line, "Product Name ="); ok {
			model = strings.TrimSpace(v)
		} else if v, ok := cutPrefix(line, "Serial Number ="); ok {
			serial = strings.TrimSpace(v)
		}
	}
	return model, serial
}

// ParseStorcliVDListText parses the text-mode "VD LIST" table
// (DG/VD TYPE State ... Size ...) from `storcli /cX/vall show`.
func ParseStorcliVDListText(ctrlID string, data []byte) []VirtualDrive {
	var vds []VirtualDrive
	inTable := false
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "DG/VD") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if t == "" || strings.HasPrefix(t, "---") || strings.HasPrefix(t, "===") {
			continue
		}
		f := strings.Fields(t)
		if len(f) < 8 || !strings.Contains(f[0], "/") {
			continue
		}
		parts := strings.SplitN(f[0], "/", 2)
		// Size is the second-to-last field before the optional Name column:
		// "... sCC Size Name" -> size spans "<number> <unit>".
		size := ""
		if len(f) >= 3 {
			size = f[len(f)-2] + " " + f[len(f)-1]
			// If last field looks like a unit (GB/TB/MB), previous is number;
			// if last field is a name (no unit), drop it.
			last := f[len(f)-1]
			if last != "GB" && last != "TB" && last != "MB" && last != "KB" {
				size = f[len(f)-3] + " " + f[len(f)-2]
			}
		}
		vds = append(vds, VirtualDrive{
			Controller: ctrlID, ID: parts[1],
			RAID: f[1], State: f[2], Size: strings.TrimSpace(size),
		})
	}
	return vds
}

// strField reads a possibly-non-string JSON value (real storcli emits
// numbers for DID, DG, etc.) as a trimmed string.
func strField(row map[string]any, key string) string {
	switch v := row[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func cutPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// bin picks storcli or storcli64, whichever exists.
func bin(ctx context.Context, r Runner) (string, error) {
	for _, b := range []string{"storcli", "storcli64", "perccli"} {
		if _, _, err := r.Run(ctx, "sh", "-c", "command -v "+b); err == nil {
			return b, nil
		}
	}
	return "", fmt.Errorf("no RAID CLI found (storcli/perccli)")
}

// showAll runs `storcli /cALL show all J` and parses it.
func showAll(ctx context.Context, r Runner) (*ShowAll, string, error) {
	b, err := bin(ctx, r)
	if err != nil {
		return nil, "", err
	}
	out, _, err := r.Run(ctx, b, "/cALL", "show", "all", "J")
	if err != nil {
		return nil, "", fmt.Errorf("%s show all: %w", b, err)
	}
	sa, err := ParseShowAll([]byte(out))
	if err != nil {
		return nil, "", err
	}
	return sa, b, nil
}

// Controllers lists controllers via `storcli /cALL show all J`.
func Controllers(ctx context.Context, r Runner) ([]Controller, error) {
	sa, b, err := showAll(ctx, r)
	if err != nil {
		return nil, err
	}
	for i := range sa.Controllers {
		sa.Controllers[i].Tool = b
	}
	return sa.Controllers, nil
}

// ListVDs returns virtual drives on a controller.
func ListVDs(ctx context.Context, r Runner, ctrlID string) ([]VirtualDrive, error) {
	sa, _, err := showAll(ctx, r)
	if err != nil {
		return nil, err
	}
	var vds []VirtualDrive
	for _, vd := range sa.VDs {
		if vd.Controller == ctrlID {
			vds = append(vds, vd)
		}
	}
	return vds, nil
}

// ListPDs returns physical drives on a controller.
func ListPDs(ctx context.Context, r Runner, ctrlID string) ([]PhysicalDrive, error) {
	sa, _, err := showAll(ctx, r)
	if err != nil {
		return nil, err
	}
	var pds []PhysicalDrive
	for _, pd := range sa.PDs {
		if pd.Controller == ctrlID {
			pds = append(pds, pd)
		}
	}
	return pds, nil
}

// CreateVD builds the command for a new VD. Drives are "e:s" pairs.
// Supported levels: 0, 1, 5, 10 (per vendor doc).
//
// Syntax (M7): the vendor's own format strings for VD creation were not
// recoverable from the binary; the command uses the storcli grammar from
// the storcli help text the vendor bundles inside collectors_amd64:
// "/cx add vd r[0|1|5|6|00|10|50|60] [Size=<VD_SIZE>] drives=e:s|e:s-x".
// (Our earlier "type=raid<level>" form is not storcli grammar and was
// corrected to "r<level>".)
func CreateVD(ctrlID, level string, drives []string) ([]string, error) {
	switch level {
	case "0", "1", "5", "10":
	default:
		return nil, fmt.Errorf("unsupported RAID level %q (want 0/1/5/10)", level)
	}
	if len(drives) == 0 {
		return nil, fmt.Errorf("no drives given")
	}
	return []string{"storcli", "/c" + ctrlID, "add", "vd", "r" + level,
		"drives=" + strings.Join(drives, ",")}, nil
}

// DeleteVD builds the VD deletion command.
func DeleteVD(ctrlID, vdID string) []string {
	return []string{"storcli", "/c" + ctrlID + "/v" + vdID, "del", "force"}
}

// QueryJSON is the agent task contract for `raid.query`.
func QueryJSON(ctx context.Context, r Runner) (string, error) {
	ctrls, err := Controllers(ctx, r)
	if err != nil {
		return "", err
	}
	type ctrlInfo struct {
		Controller
		VDs []VirtualDrive `json:"vds"`
	}
	var out []ctrlInfo
	for _, c := range ctrls {
		vds, err := ListVDs(ctx, r, c.ID)
		if err != nil {
			return "", err
		}
		out = append(out, ctrlInfo{Controller: c, VDs: vds})
	}
	b, err := json.Marshal(out)
	return string(b), err
}
