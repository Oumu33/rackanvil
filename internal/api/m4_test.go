package api_test

// M4 route tests: typed task dispatch, cfg snapshot archive flow, topology
// FDB import, relay node registry, firmware upgrade gating, RAID validation,
// WinPE template rendering, arch-aware bootscript.

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Oumu33/rackanvil/internal/api"
	"github.com/Oumu33/rackanvil/internal/db"
)

func newTestServerDB(t *testing.T) *api.Server {
	t.Helper()
	d, err := db.Open("sqlite::memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return api.New(d, d, "http://127.0.0.1:9100", log.New(io.Discard, "", 0))
}

func enrollHost(t *testing.T, srv *api.Server) (string, string) {
	t.Helper()
	code, data := mpost(t, srv, "/api/v1/client/initialize", api.InitializeRequest{
		Hostname: "m4-node", MACs: []string{"52:54:00:99:88:77"}, IPs: []string{"10.0.0.9"},
	})
	if code != 200 {
		t.Fatalf("initialize: %d %s", code, data)
	}
	var r api.InitializeResponse
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r.HostID, r.AgentToken
}

func TestM4TypedTaskDispatch(t *testing.T) {
	srv := newTestServerDB(t)
	hostID, _ := enrollHost(t, srv)
	for path, typ := range map[string]string{
		"/api/v1/locator/scan":     "disk.scan",
		"/api/v1/topology/collect": "topo.collect",
		"/api/v1/firmware/detect":  "firmware.detect",
		"/api/v1/raid/query":       "raid.query",
	} {
		code, data := mpost(t, srv, path, map[string]string{"host_id": hostID})
		if code != 200 {
			t.Fatalf("%s: status=%d body=%s", path, code, data)
		}
		var r map[string]string
		json.Unmarshal(data, &r)
		code2, data2 := getJSON(t, srv, "/api/v1/tasks/"+r["task_id"])
		if code2 != 200 {
			t.Fatalf("task get: %d", code2)
		}
		var task map[string]any
		json.Unmarshal(data2, &task)
		if task["Type"] != typ {
			t.Errorf("%s: task type=%v want %s", path, task["Type"], typ)
		}
	}
}

func TestM4FirmwareUpgradeRequiresConfirm(t *testing.T) {
	srv := newTestServerDB(t)
	hostID, _ := enrollHost(t, srv)
	code, _ := mpost(t, srv, "/api/v1/firmware/upgrade", map[string]any{
		"host_id": hostID, "model": "MegaRAID_SAS_9361_8i", "fw_file": "/tmp/fw.bin",
	})
	if code != 400 {
		t.Fatalf("upgrade without confirm: status=%d want 400", code)
	}
	code, data := mpost(t, srv, "/api/v1/firmware/upgrade", map[string]any{
		"host_id": hostID, "model": "MegaRAID_SAS_9361_8i", "fw_file": "/tmp/fw.bin", "confirm": true,
	})
	if code != 200 {
		t.Fatalf("upgrade with confirm: status=%d body=%s", code, data)
	}
}

func TestM4RaidCreateValidation(t *testing.T) {
	srv := newTestServerDB(t)
	hostID, _ := enrollHost(t, srv)
	code, _ := mpost(t, srv, "/api/v1/raid/create", map[string]any{
		"host_id": hostID, "controller": "0", "level": "6",
		"drives": []string{"252:0"}, "confirm": true,
	})
	if code != 400 {
		t.Fatalf("RAID6 must be rejected: status=%d", code)
	}
	code, _ = mpost(t, srv, "/api/v1/raid/create", map[string]any{
		"host_id": hostID, "controller": "0", "level": "1",
		"drives": []string{"252:0", "252:1"},
	})
	if code != 400 {
		t.Fatalf("create without confirm: status=%d want 400", code)
	}
}

func TestM4CfgArchiveFlow(t *testing.T) {
	srv := newTestServerDB(t)
	hostID, token := enrollHost(t, srv)
	// enqueue snapshot
	code, data := mpost(t, srv, "/api/v1/cfgarchive/snapshot", map[string]string{"host_id": hostID})
	if code != 200 {
		t.Fatalf("snapshot: %d %s", code, data)
	}
	var r map[string]string
	json.Unmarshal(data, &r)
	taskID := r["task_id"]
	// simulate the agent reporting the completed snapshot via the public API
	code, data = mpost(t, srv, "/api/v1/client/report", api.ReportRequest{
		HostID: hostID, AgentToken: token, TaskID: taskID, Status: "done",
		Stdout: `{"raid":"VD LIST","partition":"label: gpt","network":"{}","packages":""}`,
	})
	if code != 200 {
		t.Fatalf("report: %d %s", code, data)
	}
	code, data = mpost(t, srv, "/api/v1/cfgarchive/save", map[string]string{"task_id": taskID, "note": "pre-upgrade"})
	if code != 200 {
		t.Fatalf("save: %d %s", code, data)
	}
	// list
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cfgarchive/list?host_id="+hostID, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("list: %d", rec.Code)
	}
	var snaps []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &snaps)
	if len(snaps) != 1 {
		t.Fatalf("want 1 snapshot, got %d", len(snaps))
	}
	// restore without confirm -> 400
	code, _ = mpost(t, srv, "/api/v1/cfgarchive/restore", map[string]string{"snapshot_id": snaps[0]["ID"].(string)})
	if code != 400 {
		t.Fatalf("restore without confirm: status=%d want 400", code)
	}
}

func TestM4TopoFDBImport(t *testing.T) {
	srv := newTestServerDB(t)
	code, data := mpost(t, srv, "/api/v1/topology/fdb-import", map[string]any{
		"host_id": "host-x", "macs": []string{"AA:BB:CC:DD:EE:FF"},
		"entries": []map[string]any{{"switch_name": "SW-1", "port": "Gi1/0/1", "mac": "aa:bb:cc:dd:ee:ff", "vlan": 10}},
	})
	if code != 200 {
		t.Fatalf("fdb-import: %d %s", code, data)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/topology/links", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var links []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &links)
	if len(links) != 1 || links[0]["SwitchPort"] != "Gi1/0/1" {
		t.Fatalf("unexpected links %s", rec.Body.String())
	}
}

func TestM4RelayNodes(t *testing.T) {
	srv := newTestServerDB(t)
	code, _ := mpost(t, srv, "/api/v1/relay/nodes", map[string]any{
		"name": "relay-sh", "subnets": []string{"10.20.1.0/24"},
	})
	if code != 201 {
		t.Fatalf("register: status=%d want 201", code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/relay/nodes", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var nodes []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &nodes)
	if len(nodes) != 1 || nodes[0]["Name"] != "relay-sh" {
		t.Fatalf("unexpected nodes %s", rec.Body.String())
	}
}

func TestM4WinPEBootRenders(t *testing.T) {
	srv := newTestServerDB(t)
	srv.SetAssetDir("../../assets")
	req := httptest.NewRequest(http.MethodGet, "/winpe/boot.ipxe?mac=aa:bb:cc:dd:ee:ff", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("winpe boot.ipxe: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "wimboot") || !strings.Contains(body, "aa:bb:cc:dd:ee:ff") {
		t.Fatalf("bad template render: %q", body)
	}
	// licensed asset absent -> 404 with honest message
	req2 := httptest.NewRequest(http.MethodGet, "/winpe/boot.wim", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != 404 || !strings.Contains(rec2.Body.String(), "licensed") {
		t.Fatalf("boot.wim: status=%d body=%q", rec2.Code, rec2.Body.String())
	}
}

// M7: vendor-verbatim winpeshl route + rendered unattend + boot chain
// carrying winpeshl.ini as an initrd.
func TestM7WinPEChain(t *testing.T) {
	srv := newTestServerDB(t)
	srv.SetAssetDir("../../assets")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ipxe/win/winpeshl.ini?mac=aa:bb:cc:dd:ee:ff", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("winpeshl.ini: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// M8: the vendor's winpeshl.ini body is exactly "[LaunchApp]" +
	// AppPath pointing at install.bat (recovered from the vendor
	// binary); the SMB/net-use work lives in install.bat, not here.
	if !strings.Contains(body, "[LaunchApp]") || !strings.Contains(body, `AppPath = %SYSTEMDRIVE%\Windows\System32\install.bat`) {
		t.Fatalf("bad winpeshl render: %q", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/winpe/autounattend.xml?hostname=TEST-PC", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "TEST-PC") || !strings.Contains(rec.Body.String(), "<unattend") {
		t.Fatalf("autounattend: %d body=%q", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/winpe/boot.ipxe?mac=aa:bb:cc:dd:ee:ff", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/api/v1/ipxe/win/winpeshl.ini") {
		t.Fatalf("boot.ipxe must chain winpeshl.ini: %d %q", rec.Code, rec.Body.String())
	}
}

func TestM4BootScriptArch(t *testing.T) {
	srv := newTestServerDB(t)
	for arch, want := range map[string]string{"aarch64": "arm64/linux", "": "vmlinuz"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/ipxe/bootscript?mac=aa:bb:cc:dd:ee:ff&arch="+arch, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("bootscript arch=%q: %d", arch, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("arch=%q: script missing %q:\n%s", arch, want, rec.Body.String())
		}
	}
}

func mpost(t *testing.T, srv *api.Server, path string, body any) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func getJSON(t *testing.T, srv *api.Server, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestM4DeepHealthAlarmPath(t *testing.T) {
	srv := newTestServerDB(t)
	hostID, token := enrollHost(t, srv)
	// upload metrics with a UE memory error -> expect platform.deep alarm
	snap := map[string]any{
		"Deep": map[string]any{"mem_ce": 0, "mem_ue": 2, "aer_correctable": 0, "aer_fatal": 0, "aer_nonfatal": 0, "mce_available": true},
	}
	code, _ := mpost(t, srv, "/api/v1/metrics", map[string]any{
		"host_id": hostID, "agent_token": token, "snapshot": snap,
	})
	if code != 200 {
		t.Fatalf("metrics: status=%d", code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hosts/"+hostID+"/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var h struct {
		ActiveAlarms []struct {
			Kind     string
			Severity string
		} `json:"active_alarms"`
	}
	json.Unmarshal(rec.Body.Bytes(), &h)
	found := false
	for _, a := range h.ActiveAlarms {
		if a.Kind == "platform.deep" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected platform.deep alarm, got %s", rec.Body.String()[:300])
	}
}

func TestM9WinInstallBatJIT(t *testing.T) {
	srv := newTestServerDB(t)
	// No mac -> vendor's "MAC required".
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ipxe/win/install.bat", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("no-mac: %d", rec.Code)
	}
	// Unknown mac -> vendor's "Task not found".
	req = httptest.NewRequest(http.MethodGet, "/api/v1/ipxe/win/install.bat?mac=00:11:22:33:44:55", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("unknown-mac: %d", rec.Code)
	}
}
