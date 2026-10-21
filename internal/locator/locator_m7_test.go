package locator

// M7: structured storcli locate (vendor "/cx/ex/sx start locate" form).

import (
	"context"
	"strings"
	"testing"
)

func TestLocateStorcliStructured(t *testing.T) {
	m := &mockRunner{which: map[string]bool{"storcli64": true}}
	d := Disk{Device: "/dev/sdb", Tool: "storcli", Ctrl: 0, Enc: 8, Slot: 3}
	res, err := Locate(context.Background(), m, d, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tool != "storcli64" {
		t.Errorf("tool = %q, want storcli64", res.Tool)
	}
	// The invocation must be structured argv (no shell): exactly one call
	// "storcli64 /c0/e8/s3 start locate".
	want := "storcli64 /c0/e8/s3 start locate"
	found := false
	for _, c := range m.calls {
		if c == want {
			found = true
		}
		if strings.HasPrefix(c, "sh ") && strings.Contains(c, "locate") {
			t.Errorf("structured locate must not go through sh: %q", c)
		}
	}
	if !found {
		t.Errorf("calls = %v, want %q", m.calls, want)
	}

	// Off verb.
	m2 := &mockRunner{which: map[string]bool{"storcli64": true}}
	if _, err := Locate(context.Background(), m2, d, false); err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, c := range m2.calls {
		if c == "storcli64 /c0/e8/s3 stop locate" {
			ok = true
		}
	}
	if !ok {
		t.Errorf("off calls = %v", m2.calls)
	}

	// Legacy free-form enclosure still works when numbering is unknown.
	m3 := &mockRunner{which: map[string]bool{}}
	dLegacy := Disk{Device: "/dev/sdb", Tool: "storcli", Enclosure: "/c0/e8/s3", Ctrl: -1, Enc: -1, Slot: -1}
	if _, err := Locate(context.Background(), m3, dLegacy, true); err != nil {
		t.Errorf("legacy path: %v", err)
	}
}
