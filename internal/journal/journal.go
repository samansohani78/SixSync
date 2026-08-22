// Package journal records what an apply did, and what the state was before it.
//
// The journal is written incrementally, before and after each mutation, so that
// a process killed mid-apply still leaves a usable record. That record is what
// `sixsync rollback` reads, and what an operator reads when a partial failure
// needs untangling by hand.
//
// It is deliberately append-structured: entries are written as they happen
// rather than assembled at the end, because the failure this exists for is
// exactly the one where "the end" never arrives.
package journal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/samansohani78/SixSync/internal/model"
)

// FormatVersion is the journal's schema version.
const FormatVersion = "sixsync.journal/v1alpha1"

// EntryType is what a record describes.
type EntryType string

const (
	// EntryHeader opens a journal and identifies the apply.
	EntryHeader EntryType = "header"
	// EntryBefore records a resource's state immediately before it is changed.
	EntryBefore EntryType = "before"
	// EntryResult records the outcome of one mutation.
	EntryResult EntryType = "result"
	// EntryFooter closes a journal with the overall outcome.
	EntryFooter EntryType = "footer"
)

// Outcome is how one operation, or a whole apply, ended.
type Outcome string

const (
	// OutcomeApplied means the mutation succeeded and was verified by reading
	// the resource back.
	OutcomeApplied Outcome = "applied"
	// OutcomeFailed means the mutation was attempted and rejected.
	OutcomeFailed Outcome = "failed"
	// OutcomeUncertain means the request was sent but its outcome could not be
	// determined — a timeout or a dropped connection. This is the state that
	// matters most: it must never be reported as either success or failure.
	OutcomeUncertain Outcome = "uncertain"
	// OutcomeSkipped means the operation was not attempted, because an earlier
	// one failed.
	OutcomeSkipped Outcome = "skipped"
	// OutcomeVerifyFailed means the write was accepted but reading it back did
	// not return what was written.
	OutcomeVerifyFailed Outcome = "verify-failed"
)

// Header identifies an apply run.
type Header struct {
	FormatVersion string    `json:"format_version"`
	StartedAt     time.Time `json:"started_at"`

	// PlanDigest ties this journal to the plan that produced it.
	PlanDigest string `json:"plan_digest"`
	// AdminURL is the gateway that was changed. Rollback checks it.
	AdminURL string `json:"admin_url"`
	// APISIXVersion is what the gateway reported.
	APISIXVersion string `json:"apisix_version,omitempty"`
	// KeyFingerprint identifies the credential without disclosing it.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	// SixSyncVersion is the build that ran the apply.
	SixSyncVersion string `json:"sixsync_version,omitempty"`
	// OperationCount is how many mutations were planned.
	OperationCount int `json:"operation_count"`
}

// Before is a resource's state prior to being changed.
//
// The state is stored redacted. A journal is written to disk and often kept as
// a CI artifact, so it must not become the place credentials end up — which
// means rollback cannot restore a secret value it was never given, and says so
// rather than writing a placeholder into the gateway.
type Before struct {
	Kind model.Kind `json:"kind"`
	ID   string     `json:"id"`

	// Existed reports whether the resource was present before the change. A
	// create records false, which is what tells rollback to delete it.
	Existed bool `json:"existed"`

	// State is the redacted prior body, absent when the resource did not exist.
	State map[string]any `json:"state,omitempty"`

	// Digest is the canonical digest of the *unredacted* prior state, so
	// rollback can tell whether the resource still looks like what was
	// recorded even though the recorded copy is redacted.
	Digest string `json:"digest,omitempty"`

	// ModifiedIndex is the revision before the change.
	ModifiedIndex int64 `json:"modified_index,omitempty"`

	// RedactedFields are paths whose values were removed from State. Rollback
	// refuses to restore these, because it does not have them.
	RedactedFields []string `json:"redacted_fields,omitempty"`
}

// Result is the outcome of one mutation.
type Result struct {
	Order   int        `json:"order"`
	Type    string     `json:"type"`
	Kind    model.Kind `json:"kind"`
	ID      string     `json:"id"`
	Outcome Outcome    `json:"outcome"`

	// Error is the failure message, already free of credentials because the
	// client never puts a request body in an error.
	Error string `json:"error,omitempty"`

	// ModifiedIndexAfter is the revision after a successful write.
	ModifiedIndexAfter int64 `json:"modified_index_after,omitempty"`

	At time.Time `json:"at"`
}

// Footer closes a journal.
type Footer struct {
	FinishedAt time.Time `json:"finished_at"`
	Outcome    Outcome   `json:"outcome"`
	Applied    int       `json:"applied"`
	Failed     int       `json:"failed"`
	Skipped    int       `json:"skipped"`
	Uncertain  int       `json:"uncertain"`
	// Message explains the overall outcome in one line.
	Message string `json:"message,omitempty"`
}

// Entry is one record in a journal.
type Entry struct {
	Type   EntryType `json:"type"`
	Header *Header   `json:"header,omitempty"`
	Before *Before   `json:"before,omitempty"`
	Result *Result   `json:"result,omitempty"`
	Footer *Footer   `json:"footer,omitempty"`
}

// Writer appends entries to a journal file.
//
// Every entry is flushed and synced as it is written. That costs a syscall per
// mutation, which is irrelevant next to an Admin API round trip, and it is the
// difference between a usable journal and an empty file if the process dies.
type Writer struct {
	f   *os.File
	enc *json.Encoder
}

// Create opens a new journal for writing.
func Create(path string) (*Writer, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	// 0600: a journal describes a production gateway's configuration.
	// The path is chosen by the operator; writing there is the point.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: intentional
	if err != nil {
		return nil, fmt.Errorf("creating journal: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return &Writer{f: f, enc: enc}, nil
}

// Append writes one entry and forces it to disk.
func (w *Writer) Append(e Entry) error {
	if err := w.enc.Encode(e); err != nil {
		return fmt.Errorf("writing journal entry: %w", err)
	}
	// Sync on every entry: this record exists for the case where the process
	// does not survive to close the file.
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("syncing journal: %w", err)
	}
	return nil
}

// Close closes the journal file.
func (w *Writer) Close() error { return w.f.Close() }

// Journal is a parsed journal.
type Journal struct {
	Header  Header
	Befores []Before
	Results []Result
	Footer  *Footer
}

// BeforeFor returns the recorded prior state of one resource.
func (j *Journal) BeforeFor(kind model.Kind, id string) *Before {
	for i := range j.Befores {
		if j.Befores[i].Kind == kind && j.Befores[i].ID == id {
			return &j.Befores[i]
		}
	}
	return nil
}

// Read parses a journal.
//
// A journal with no footer is not an error: that is precisely what an
// interrupted apply leaves behind, and it is the case rollback exists for.
func Read(r io.Reader) (*Journal, error) {
	scanner := bufio.NewScanner(r)
	// Journal lines hold a full resource body, which can be large.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var j Journal
	var sawHeader bool

	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}

		var e Entry
		dec := json.NewDecoder(bytesReader(raw))
		dec.UseNumber()
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			// A truncated final line is what a killed process leaves. Earlier
			// entries are still trustworthy, so keep them and stop here.
			if sawHeader {
				break
			}
			return nil, fmt.Errorf("journal line %d: %w", line, err)
		}

		switch e.Type {
		case EntryHeader:
			if e.Header == nil {
				return nil, fmt.Errorf("journal line %d: header entry has no header", line)
			}
			j.Header = *e.Header
			sawHeader = true
		case EntryBefore:
			if e.Before != nil {
				j.Befores = append(j.Befores, *e.Before)
			}
		case EntryResult:
			if e.Result != nil {
				j.Results = append(j.Results, *e.Result)
			}
		case EntryFooter:
			j.Footer = e.Footer
		default:
			return nil, fmt.Errorf("journal line %d: unknown entry type %q", line, e.Type)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading journal: %w", err)
	}
	if !sawHeader {
		return nil, fmt.Errorf("journal has no header; it is not a SixSync journal")
	}
	if j.Header.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("journal format %q is not supported by this build, which "+
			"implements %q", j.Header.FormatVersion, FormatVersion)
	}
	return &j, nil
}

// Open reads a journal from a file.
func Open(path string) (*Journal, error) {
	f, err := os.Open(path) //nolint:gosec // G304: an operator-specified path is the point
	if err != nil {
		return nil, fmt.Errorf("opening journal: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only
	return Read(f)
}

// Complete reports whether the apply ran to completion.
func (j *Journal) Complete() bool { return j.Footer != nil }
