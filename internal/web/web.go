// Package web implements the M2 web console MVP: login (bcrypt+JWT),
// host inventory, shell task dispatch and result viewing.
//
// Server-rendered HTML with inline templates — intentionally spartan.
// RBAC: viewer can read; operator/admin can dispatch tasks.
package web

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Oumu33/rackanvil/internal/auth"
	"github.com/Oumu33/rackanvil/internal/db"
	"github.com/Oumu33/rackanvil/internal/store"
)

// TaskStore is the persistence surface the console needs for tasks/hosts.
type TaskStore interface {
	EnqueueTask(hostID, command string, timeoutSec int) (*store.Task, error)
	// EnqueueTypedTask adds a non-shell task (M4: locator/raid/firmware/...).
	EnqueueTypedTask(hostID, typ, command string, timeoutSec int) (*store.Task, error)
	GetTask(taskID string) (*store.Task, error)
	Hosts() []*store.Host
	Tasks() []*store.Task
}

type ctxKey struct{}

// Web is the console handler set.
type Web struct {
	tasks  TaskStore
	db     *db.DB
	signer *auth.Signer
	mux    *http.ServeMux
	logger *log.Logger
	licPub ed25519.PublicKey // nil = dev mode (set by RegisterM3)
}

func New(tasks TaskStore, database *db.DB, signer *auth.Signer, logger *log.Logger) *Web {
	w := &Web{tasks: tasks, db: database, signer: signer, mux: http.NewServeMux(), logger: logger}
	w.mux.HandleFunc("/login", w.handleLogin)
	w.mux.HandleFunc("/logout", w.handleLogout)
	w.mux.HandleFunc("/", w.requireLogin(w.handleIndex))
	w.mux.HandleFunc("/hosts", w.requireLogin(w.handleHosts))
	w.mux.HandleFunc("/hosts/", w.requireLogin(w.handleHostDetail))
	w.mux.HandleFunc("/tasks", w.requireLogin(w.handleTasks))
	w.mux.HandleFunc("/tasks/", w.requireLogin(w.handleTaskDetail))
	return w
}

func (w *Web) Handler() http.Handler { return w.mux }

// EnsureAdmin creates the initial admin account on first boot. The password
// is random, printed to the server log exactly once, and never hardcoded.
func (w *Web) EnsureAdmin() {
	n, err := w.db.UserCount()
	if err != nil {
		w.logger.Printf("web: user count check failed: %v", err)
		return
	}
	if n > 0 {
		return
	}
	pw := auth.RandomPassword()
	hash, err := auth.HashPassword(pw)
	if err != nil {
		w.logger.Printf("web: admin bootstrap failed: %v", err)
		return
	}
	if err := w.db.CreateUser("admin", hash, "admin"); err != nil {
		w.logger.Printf("web: admin bootstrap failed: %v", err)
		return
	}
	_ = w.db.Audit("system", "admin.bootstrap", "initial admin created")
	w.logger.Printf("================================================================")
	w.logger.Printf("web: INITIAL ADMIN ACCOUNT CREATED")
	w.logger.Printf("web:   username: admin")
	w.logger.Printf("web:   password: %s", pw)
	w.logger.Printf("web:   (shown once — change it after first login)")
	w.logger.Printf("================================================================")
}

func userOf(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxKey{}).(*store.User)
	return u
}

func (w *Web) requireLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("sess")
		if err != nil {
			http.Redirect(rw, r, "/login", http.StatusFound)
			return
		}
		claims, err := w.signer.Verify(c.Value)
		if err != nil {
			http.Redirect(rw, r, "/login", http.StatusFound)
			return
		}
		u, err := w.db.GetUser(claims.Username)
		if err != nil {
			http.Redirect(rw, r, "/login", http.StatusFound)
			return
		}
		next(rw, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

func (w *Web) canOperate(u *store.User) bool { return u.Role == "admin" || u.Role == "operator" }

func (w *Web) render(rw http.ResponseWriter, name string, data any) {
	t := template.Must(template.New("page").Parse(pageTmpl + m4Tmpl))
	var sb strings.Builder
	if err := t.ExecuteTemplate(&sb, name, data); err != nil {
		http.Error(rw, "template: "+err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Write([]byte(sb.String()))
}

// ---- handlers ----

func (w *Web) handleLogin(rw http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		username := r.FormValue("username")
		password := r.FormValue("password")
		u, err := w.db.GetUser(username)
		if err != nil || !auth.CheckPassword(password, u.PasswordHash) {
			_ = w.db.Audit(username, "login.failed", r.RemoteAddr)
			w.render(rw, "login", map[string]any{"Error": "用户名或密码错误"})
			return
		}
		tok, err := w.signer.Sign(u.ID, u.Username, u.Role, 12*time.Hour)
		if err != nil {
			http.Error(rw, "token", http.StatusInternalServerError)
			return
		}
		http.SetCookie(rw, &http.Cookie{
			Name: "sess", Value: tok, Path: "/",
			Expires: time.Now().Add(12 * time.Hour), HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		_ = w.db.Audit(u.Username, "login.ok", r.RemoteAddr)
		http.Redirect(rw, r, "/hosts", http.StatusFound)
		return
	}
	w.render(rw, "login", nil)
}

func (w *Web) handleLogout(rw http.ResponseWriter, r *http.Request) {
	http.SetCookie(rw, &http.Cookie{Name: "sess", Path: "/", MaxAge: -1})
	http.Redirect(rw, r, "/login", http.StatusFound)
}

func (w *Web) handleIndex(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}
	http.Redirect(rw, r, "/hosts", http.StatusFound)
}

type hostRow struct {
	*store.Host
	LastCheckinAgo string
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "从未"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	}
}

func (w *Web) handleHosts(rw http.ResponseWriter, r *http.Request) {
	var rows []hostRow
	for _, h := range w.tasks.Hosts() {
		rows = append(rows, hostRow{Host: h, LastCheckinAgo: ago(h.LastCheckin)})
	}
	w.render(rw, "hosts", map[string]any{"User": userOf(r), "Hosts": rows})
}

type hostDetailData struct {
	User    *store.User
	Host    *store.Host
	Metrics *store.MetricSample
	Alarms  []store.Alarm
	Tasks   []*store.Task
}

func (w *Web) handleHostDetail(rw http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/hosts/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(rw, r)
		return
	}
	var h *store.Host
	for _, x := range w.tasks.Hosts() {
		if x.ID == id {
			h = x
			break
		}
	}
	if h == nil {
		http.NotFound(rw, r)
		return
	}
	m, _ := w.db.LatestMetrics(id)
	alarms, _ := w.db.ActiveAlarms(id)
	var tasks []*store.Task
	for _, t := range w.tasks.Tasks() {
		if t.HostID == id {
			tasks = append(tasks, t)
		}
	}
	w.render(rw, "host_detail", hostDetailData{
		User: userOf(r), Host: h, Metrics: m, Alarms: alarms, Tasks: tasks,
	})
}

func (w *Web) handleTasks(rw http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	if r.Method == http.MethodPost {
		if !w.canOperate(u) {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		hostID := r.FormValue("host_id")
		cmd := r.FormValue("command")
		if hostID == "" || cmd == "" {
			http.Error(rw, "host_id and command required", http.StatusBadRequest)
			return
		}
		t, err := w.tasks.EnqueueTask(hostID, cmd, 120)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		_ = w.db.Audit(u.Username, "task.enqueue", hostID+" "+cmd)
		http.Redirect(rw, r, "/tasks/"+t.ID, http.StatusFound)
		return
	}
	http.Redirect(rw, r, "/hosts", http.StatusFound)
}

func (w *Web) handleTaskDetail(rw http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/tasks/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(rw, r)
		return
	}
	t, err := w.tasks.GetTask(id)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	w.render(rw, "task_detail", map[string]any{"User": userOf(r), "Task": t})
}
