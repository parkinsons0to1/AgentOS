// Package dash is the human viewport: a small web dashboard that renders
// sessions from their state directories (meta.json + ledger.jsonl) and offers
// a kill switch. It reads what the system already wrote — it is never in the
// agent's execution path, and no agent ever looks at it.
package dash

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/parkinsons0to1/AgentOS/internal/ledger"
	"github.com/parkinsons0to1/AgentOS/internal/session"
)

// Handler serves the dashboard for one state root.
func Handler(stateRoot string) http.Handler {
	d := &dash{stateRoot: stateRoot}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", d.index)
	mux.HandleFunc("GET /api/sessions", d.listSessions)
	mux.HandleFunc("GET /api/sessions/{id}/ledger", d.readLedger)
	mux.HandleFunc("POST /api/sessions/{id}/kill", d.killSession)
	return mux
}

type dash struct {
	stateRoot string
}

// sessionView is one row in the session list.
type sessionView struct {
	session.Meta
	Status string `json:"status"` // running | finished | unknown
}

func (d *dash) sessionDir(id string) (string, bool) {
	// IDs come from URLs; keep them inside the state root.
	if id == "" || strings.ContainsAny(id, "/\\") || id == ".." {
		return "", false
	}
	return filepath.Join(d.stateRoot, id), true
}

func loadMeta(dir string) (session.Meta, error) {
	var m session.Meta
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(raw, &m)
	return m, err
}

func status(m session.Meta) string {
	if m.PID == 0 {
		return "finished"
	}
	// Signal 0 probes liveness without touching the process.
	if err := syscall.Kill(m.PID, 0); err == nil {
		return "running"
	}
	return "unknown" // stale meta: process gone without a final rewrite
}

func (d *dash) listSessions(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(d.stateRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := []sessionView{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := loadMeta(filepath.Join(d.stateRoot, e.Name()))
		if err != nil {
			continue // not a session dir (or session still initializing)
		}
		views = append(views, sessionView{Meta: m, Status: status(m)})
	}
	sort.Slice(views, func(i, j int) bool {
		return views[i].StartedAt.After(views[j].StartedAt)
	})
	writeJSON(w, http.StatusOK, map[string]any{"sessions": views})
}

// readLedger returns events from the session's ledger starting at ?offset=N
// (an event index), plus the next offset — a poll-friendly tail.
func (d *dash) readLedger(w http.ResponseWriter, r *http.Request) {
	dir, ok := d.sessionDir(r.PathValue("id"))
	if !ok {
		httpError(w, http.StatusBadRequest, "bad session id")
		return
	}
	events, err := ledger.Read(dir)
	if err != nil {
		httpError(w, http.StatusNotFound, "no ledger for session")
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 || offset > len(events) {
		offset = len(events)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events[offset:],
		"next":   len(events),
	})
}

func (d *dash) killSession(w http.ResponseWriter, r *http.Request) {
	dir, ok := d.sessionDir(r.PathValue("id"))
	if !ok {
		httpError(w, http.StatusBadRequest, "bad session id")
		return
	}
	m, err := loadMeta(dir)
	if err != nil {
		httpError(w, http.StatusNotFound, "no such session")
		return
	}
	if status(m) != "running" {
		httpError(w, http.StatusConflict, "session is not running")
		return
	}
	if err := syscall.Kill(m.PID, syscall.SIGTERM); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"killed": m.ID, "pid": m.PID})
}

func (d *dash) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body)
}
