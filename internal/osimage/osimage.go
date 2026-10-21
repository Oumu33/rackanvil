// Package osimage identifies OS installation images using the
// vendor's definition set (M9): 54 JSON definitions extracted in full
// from the vendor server binary's embedded FS
// (deplos-reversing/m9-evidence/definitions/).
//
// Vendor identification algorithm (from the definition schema):
//   - read the definition's identity file inside the image tree
//     (identityFilePath; "|" separates alternative paths, e.g.
//     Windows accepts sources/install.wim or sources/install.esd);
//   - the file's content must match identityFileContentRegex;
//   - every non-empty entry of validationFiles must exist in the tree.
//
// A match classifies the image into (family, majorVersion), which the
// vendor uses to select the unattended-install template and mirror
// layout. The proxmox definition ships in the vendor build renamed to
// proxmox.json.bak (disabled). M11 disposition: the definition still
// loads (catalogue fidelity) but is flagged Disabled and skipped by
// Identify — shipping it silently active, as a naive embed of the
// vendor set would, misrepresents what the vendor product does.
package osimage

import (
	"embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

//go:embed definitions/*.json definitions/*.json.bak
var definitionsFS embed.FS

// Definition is one vendor OS-image identification definition.
type Definition struct {
	ID                       string   `json:"id"`
	Name                     string   `json:"name"`
	Family                   string   `json:"family"`
	MajorVersion             int      `json:"majorVersion"`
	IdentityFilePath         string   `json:"identityFilePath"`
	IdentityFileContentRegex string   `json:"identityFileContentRegex"`
	ValidationFiles          []string `json:"validationFiles"`
	// Disabled marks definitions the vendor shipped deactivated (file
	// renamed to .json.bak). M11: Identify skips them — the vendor's
	// build silently relied on the rename; we keep the definition for
	// reference but say out loud that it does not match.
	Disabled bool `json:"-"`
}

// Definitions returns the full vendor definition set, sorted by file
// name for deterministic matching.
func Definitions() ([]Definition, error) {
	entries, err := definitionsFS.ReadDir("definitions")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]Definition, 0, len(names))
	for _, n := range names {
		b, err := definitionsFS.ReadFile("definitions/" + n)
		if err != nil {
			return nil, err
		}
		var d Definition
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, err
		}
		d.Disabled = strings.HasSuffix(n, ".bak")
		out = append(out, d)
	}
	return out, nil
}

// Identify classifies an image tree. files maps slash-separated
// relative paths to file contents (presence in the map = existence;
// content is only read for identity files). Returns the first
// matching definition in file-name order.
func Identify(files map[string]string) (*Definition, bool) {
	defs, err := Definitions()
	if err != nil {
		return nil, false
	}
	for i := range defs {
		if defs[i].Disabled {
			continue // vendor shipped it deactivated (.bak); do not match
		}
		if matches(&defs[i], files) {
			return &defs[i], true
		}
	}
	return nil, false
}

func matches(d *Definition, files map[string]string) bool {
	// Identity: any alternative path whose content matches the regex.
	identityOK := false
	for _, p := range strings.Split(d.IdentityFilePath, "|") {
		content, ok := files[p]
		if !ok {
			continue
		}
		re, err := regexp.Compile(d.IdentityFileContentRegex)
		if err != nil {
			return false // vendor regex not RE2-compatible: cannot match
		}
		if re.MatchString(content) {
			identityOK = true
			break
		}
	}
	if !identityOK {
		return false
	}
	for _, vf := range d.ValidationFiles {
		if vf == "" {
			continue
		}
		if _, ok := files[vf]; !ok {
			return false
		}
	}
	return true
}
