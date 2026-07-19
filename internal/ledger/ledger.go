// Package ledger is the append-only structured audit log — the agent-OS
// analog of dmesg. One JSONL file per session; every component writes
// through it and nothing else writes to it.
package ledger

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is one ledger record.
type Event struct {
	TS      time.Time      `json:"ts"`
	Session string         `json:"session"`
	Type    string         `json:"type"`
	Data    map[string]any `json:"data,omitempty"`
}

// Ledger appends events to a session's JSONL file, safe for concurrent use.
type Ledger struct {
	mu      sync.Mutex
	f       *os.File
	session string
}

// Open creates (or appends to) the ledger file for a session under dir.
func Open(dir, session string) (*Ledger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "ledger.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Ledger{f: f, session: session}, nil
}

// Emit writes one event. Errors are returned but callers on hot paths may
// choose to drop them; the ledger must never take down the session.
func (l *Ledger) Emit(eventType string, data map[string]any) error {
	e := Event{TS: time.Now().UTC(), Session: l.session, Type: eventType, Data: data}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err = l.f.Write(append(line, '\n'))
	return err
}

// Close flushes and closes the underlying file.
func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// Read loads all events from a session ledger file (for tests and tooling).
func Read(dir string) ([]Event, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		return nil, err
	}
	var events []Event
	dec := json.NewDecoder(bytes.NewReader(raw))
	for dec.More() {
		var e Event
		if err := dec.Decode(&e); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}
