package journal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samansohani78/SixSync/internal/model"
)

func fixedTime() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) }

func writeSample(t *testing.T, path string) {
	t.Helper()
	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer w.Close() //nolint:errcheck

	entries := []Entry{
		{Type: EntryHeader, Header: &Header{
			FormatVersion: FormatVersion, StartedAt: fixedTime(),
			PlanDigest: "sha256:abc", AdminURL: "http://gw.example.test:9180",
			APISIXVersion: "3.18.0", OperationCount: 2,
		}},
		{Type: EntryBefore, Before: &Before{
			Kind: model.KindRoute, ID: "r1", Existed: true,
			State: map[string]any{"uri": "/old"}, Digest: "sha256:old", ModifiedIndex: 41,
		}},
		{Type: EntryResult, Result: &Result{
			Order: 0, Type: "update", Kind: model.KindRoute, ID: "r1",
			Outcome: OutcomeApplied, ModifiedIndexAfter: 42, At: fixedTime(),
		}},
		{Type: EntryBefore, Before: &Before{
			Kind: model.KindUpstream, ID: "u1", Existed: false,
		}},
		{Type: EntryResult, Result: &Result{
			Order: 1, Type: "create", Kind: model.KindUpstream, ID: "u1",
			Outcome: OutcomeApplied, At: fixedTime(),
		}},
		{Type: EntryFooter, Footer: &Footer{
			FinishedAt: fixedTime(), Outcome: OutcomeApplied, Applied: 2,
			Message: "2 operation(s) applied and verified",
		}},
	}
	for _, e := range entries {
		if err := w.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	writeSample(t, path)

	j, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if j.Header.PlanDigest != "sha256:abc" {
		t.Errorf("plan digest = %q", j.Header.PlanDigest)
	}
	if j.Header.AdminURL != "http://gw.example.test:9180" {
		t.Errorf("admin URL = %q", j.Header.AdminURL)
	}
	if len(j.Befores) != 2 || len(j.Results) != 2 {
		t.Errorf("got %d befores and %d results, want 2 and 2", len(j.Befores), len(j.Results))
	}
	if !j.Complete() {
		t.Error("a journal with a footer should report as complete")
	}
	if j.Footer.Applied != 2 {
		t.Errorf("footer applied = %d", j.Footer.Applied)
	}
}

func TestBeforeForFindsRecordedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	writeSample(t, path)

	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	got := j.BeforeFor(model.KindRoute, "r1")
	if got == nil {
		t.Fatal("the recorded prior state was not found")
	}
	if !got.Existed || got.State["uri"] != "/old" || got.ModifiedIndex != 41 {
		t.Errorf("prior state = %+v", got)
	}

	if j.BeforeFor(model.KindRoute, "nonexistent") != nil {
		t.Error("BeforeFor invented a record")
	}
	// A different kind with the same identifier must not match.
	if j.BeforeFor(model.KindService, "r1") != nil {
		t.Error("BeforeFor matched across kinds")
	}
}

// An interrupted apply is exactly the case this format exists for.
func TestReadWithoutFooterIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	writeSample(t, path)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if err := os.WriteFile(path, []byte(strings.Join(lines[:len(lines)-1], "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	j, err := Open(path)
	if err != nil {
		t.Fatalf("a footerless journal must still parse: %v", err)
	}
	if j.Complete() {
		t.Error("a journal with no footer must not report as complete")
	}
	if len(j.Results) != 2 {
		t.Errorf("earlier entries should survive: got %d results", len(j.Results))
	}
}

// A half-written final line is what a killed process leaves behind.
func TestReadToleratesTruncatedFinalLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	writeSample(t, path)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw[:len(raw)-25], 0o600); err != nil {
		t.Fatal(err)
	}

	j, err := Open(path)
	if err != nil {
		t.Fatalf("a truncated journal must still yield what is intact: %v", err)
	}
	if j.Header.PlanDigest == "" {
		t.Error("the header should have survived")
	}
}

func TestReadRejectsMalformedJournals(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"no header", `{"type":"result","result":{"kind":"Route","id":"r1"}}` + "\n"},
		{"garbage first line", "not json\n"},
		{"unknown entry type", `{"type":"header","header":{"format_version":"sixsync.journal/v1alpha1"}}` +
			"\n" + `{"type":"nonsense"}` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Read(strings.NewReader(tt.body)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestReadRejectsUnknownFormatVersion(t *testing.T) {
	body := `{"type":"header","header":{"format_version":"sixsync.journal/v99",` +
		`"admin_url":"http://gw.example.test"}}` + "\n"
	_, err := Read(strings.NewReader(body))
	if err == nil {
		t.Fatal("a journal in an unknown format was accepted")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// Acting on records this build does not understand is how a rollback goes
// wrong.
func TestReadRejectsUnknownFields(t *testing.T) {
	body := `{"type":"header","unexpected":true,"header":{"format_version":"sixsync.journal/v1alpha1"}}` + "\n"
	if _, err := Read(strings.NewReader(body)); err == nil {
		t.Error("a journal with an unknown field was accepted")
	}
}

// A journal describes a production gateway's configuration.
func TestCreatedJournalIsNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

func TestCreateMakesMissingDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "journal.jsonl")
	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create should create intermediate directories: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the journal was not created: %v", err)
	}
}

func TestCreateFailsOnUnwritableLocation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permissions do not apply")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o750) })

	if _, err := Create(filepath.Join(dir, "journal.jsonl")); err == nil {
		t.Error("Create succeeded in a read-only directory")
	}
}

func TestOpenMissingFile(t *testing.T) {
	if _, err := Open("/nonexistent/journal.jsonl"); err == nil {
		t.Error("Open succeeded on a missing file")
	}
}

// Entries must be durable as they are written: the failure this format exists
// for is the one where the process does not survive to close the file.
func TestEntriesAreReadableBeforeClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Append(Entry{Type: EntryHeader, Header: &Header{
		FormatVersion: FormatVersion, StartedAt: fixedTime(),
		AdminURL: "http://gw.example.test:9180",
	}}); err != nil {
		t.Fatal(err)
	}

	// Read it back without closing the writer.
	j, err := Open(path)
	if err != nil {
		t.Fatalf("an entry was not durable before close: %v", err)
	}
	if j.Header.AdminURL != "http://gw.example.test:9180" {
		t.Errorf("header = %+v", j.Header)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// A resource body can be large, so the line reader must not cap at the
// default scanner buffer.
func TestLargeEntriesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}

	big := map[string]any{}
	for i := 0; i < 2000; i++ {
		big[strings.Repeat("k", 40)+string(rune('a'+i%26))+string(rune(i))] = strings.Repeat("v", 100)
	}

	for _, e := range []Entry{
		{Type: EntryHeader, Header: &Header{FormatVersion: FormatVersion, StartedAt: fixedTime()}},
		{Type: EntryBefore, Before: &Before{
			Kind: model.KindRoute, ID: "big", Existed: true, State: big,
		}},
	} {
		if err := w.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	j, err := Open(path)
	if err != nil {
		t.Fatalf("a large entry could not be read back: %v", err)
	}
	got := j.BeforeFor(model.KindRoute, "big")
	if got == nil || len(got.State) != len(big) {
		t.Errorf("large state did not survive the round trip")
	}
}

func TestOutcomeConstantsAreDistinct(t *testing.T) {
	seen := map[Outcome]bool{}
	for _, o := range []Outcome{
		OutcomeApplied, OutcomeFailed, OutcomeUncertain,
		OutcomeSkipped, OutcomeVerifyFailed,
	} {
		if seen[o] {
			t.Errorf("outcome %q is duplicated", o)
		}
		seen[o] = true
	}
}
