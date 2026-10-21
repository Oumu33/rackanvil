package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Oumu33/rackanvil/internal/store"
)

func newTestServer() *Server {
	return New(store.New(), nil, "http://127.0.0.1:9100", log.New(io.Discard, "", 0))
}

func postJSON(t *testing.T, srv *Server, path string, body any) (int, []byte) {
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

func TestFullAgentLifecycle(t *testing.T) {
	srv := newTestServer()

	// 1. initialize
	code, data := postJSON(t, srv, "/api/v1/client/initialize", InitializeRequest{
		Hostname: "test-node-1",
		MACs:     []string{"52:54:00:aa:bb:cc"},
		IPs:      []string{"192.168.100.101"},
	})
	if code != 200 {
		t.Fatalf("initialize: status=%d body=%s", code, data)
	}
	var initResp InitializeResponse
	if err := json.Unmarshal(data, &initResp); err != nil {
		t.Fatal(err)
	}
	if initResp.HostID == "" || initResp.AgentToken == "" {
		t.Fatal("initialize: missing host_id/token")
	}

	// 2. checkin with bad token -> 401
	code, _ = postJSON(t, srv, "/api/v1/client/checkin", CheckinRequest{
		HostID: initResp.HostID, AgentToken: "wrong",
	})
	if code != 401 {
		t.Fatalf("checkin with bad token: status=%d, want 401", code)
	}

	// 3. enqueue a task via debug hook
	code, data = postJSON(t, srv, "/debug/enqueue-task", map[string]any{
		"host_id": initResp.HostID, "command": "echo hi", "timeout_sec": 10,
	})
	if code != 200 {
		t.Fatalf("enqueue: status=%d body=%s", code, data)
	}
	var enq map[string]string
	json.Unmarshal(data, &enq)
	taskID := enq["task_id"]

	// 4. checkin -> should receive the task
	code, data = postJSON(t, srv, "/api/v1/client/checkin", CheckinRequest{
		HostID: initResp.HostID, AgentToken: initResp.AgentToken,
	})
	if code != 200 {
		t.Fatalf("checkin: status=%d body=%s", code, data)
	}
	var chk CheckinResponse
	json.Unmarshal(data, &chk)
	if len(chk.Tasks) != 1 || chk.Tasks[0].TaskID != taskID || chk.Tasks[0].Command != "echo hi" {
		t.Fatalf("checkin tasks mismatch: %+v", chk.Tasks)
	}

	// 5. report result
	code, _ = postJSON(t, srv, "/api/v1/client/report", ReportRequest{
		HostID: initResp.HostID, AgentToken: initResp.AgentToken,
		TaskID: taskID, Status: "done", Stdout: "hi\n", ExitCode: 0,
	})
	if code != 200 {
		t.Fatalf("report: status=%d", code)
	}

	// 6. verify via debug/tasks
	req := httptest.NewRequest(http.MethodGet, "/debug/tasks", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var tasks []store.Task
	if err := json.NewDecoder(rec.Result().Body).Decode(&tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "done" || tasks[0].Stdout != "hi\n" {
		t.Fatalf("task not recorded correctly: %+v", tasks)
	}
}

func TestBootScript(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ipxe/bootscript?mac=52:54:00:aa:bb:cc", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("bootscript: status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"#!ipxe", "52:54:00:aa:bb:cc", "kernel http://127.0.0.1:9100/ipxe/vmlinuz", "initrd http://127.0.0.1:9100/ipxe/initrd.img"} {
		if !strings.Contains(body, want) {
			t.Fatalf("bootscript missing %q:\n%s", want, body)
		}
	}

	// missing mac -> 400
	req = httptest.NewRequest(http.MethodGet, "/api/v1/ipxe/bootscript", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("bootscript without mac: status=%d, want 400", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthz: %d %s", rec.Code, rec.Body.String())
	}
}
