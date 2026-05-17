package metering

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AuditRecord is one config-change entry. Source is "file", "flag", or "api".
type AuditRecord struct {
	Time   time.Time `json:"time"`
	Source string    `json:"source"`
	Change string    `json:"change"`
}

func (s *Store) auditPath() string { return filepath.Join(s.dir, "audit.jsonl") }

// AppendAudit appends a config-change record to the audit log.
func (s *Store) AppendAudit(rec AuditRecord) error {
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("metering: marshal audit record: %w", err)
	}
	f, err := os.OpenFile(s.auditPath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("metering: open audit log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("metering: write audit log: %w", err)
	}
	return nil
}

// Audit returns the most recent n audit records, oldest first. n <= 0 returns
// every record.
func (s *Store) Audit(n int) ([]AuditRecord, error) {
	f, err := os.Open(s.auditPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("metering: open audit log: %w", err)
	}
	defer f.Close()
	var all []AuditRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var rec AuditRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return nil, fmt.Errorf("metering: parse audit log: %w", err)
		}
		all = append(all, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("metering: scan audit log: %w", err)
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}
