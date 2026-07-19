package capd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Escalation is a session's request for a capability it wasn't granted.
// It lives as a JSON file under <session dir>/escalations/<host>.json so the
// viewport (a separate process) can decide it; the gateway re-reads the file
// when the host is next requested. Approved escalations grant egress only —
// never credential injection, which always requires the operator's spec.
type Escalation struct {
	Host        string    `json:"host"`
	Reason      string    `json:"reason"`
	Status      string    `json:"status"` // pending | approved | denied
	RequestedAt time.Time `json:"requested_at"`
	DecidedAt   time.Time `json:"decided_at,omitzero"`
}

const (
	EscalationPending  = "pending"
	EscalationApproved = "approved"
	EscalationDenied   = "denied"
)

func escalationPath(dir, host string) string {
	return filepath.Join(dir, "escalations", host+".json")
}

// LoadEscalation reads one escalation; os.ErrNotExist when never requested.
func LoadEscalation(dir, host string) (*Escalation, error) {
	if strings.ContainsAny(host, "/\\") {
		return nil, errors.New("bad host")
	}
	raw, err := os.ReadFile(escalationPath(dir, host))
	if err != nil {
		return nil, err
	}
	var e Escalation
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// SaveEscalation writes an escalation file.
func SaveEscalation(dir string, e *Escalation) error {
	if strings.ContainsAny(e.Host, "/\\") {
		return errors.New("bad host")
	}
	if err := os.MkdirAll(filepath.Join(dir, "escalations"), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(escalationPath(dir, e.Host), raw, 0o644)
}

// ListEscalations returns all escalations for a session dir.
func ListEscalations(dir string) ([]*Escalation, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "escalations"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Escalation
	for _, en := range entries {
		host := strings.TrimSuffix(en.Name(), ".json")
		e, err := LoadEscalation(dir, host)
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// Decide sets an escalation's status. It fails if none is pending.
func Decide(dir, host string, approve bool) (*Escalation, error) {
	e, err := LoadEscalation(dir, host)
	if err != nil {
		return nil, fmt.Errorf("no escalation for %q: %w", host, err)
	}
	if e.Status != EscalationPending {
		return nil, fmt.Errorf("escalation for %q already %s", host, e.Status)
	}
	e.Status = EscalationDenied
	if approve {
		e.Status = EscalationApproved
	}
	e.DecidedAt = time.Now().UTC()
	if err := SaveEscalation(dir, e); err != nil {
		return nil, err
	}
	return e, nil
}
