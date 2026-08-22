package journal

import (
	"strings"
	"testing"
)

// FuzzReadJournal checks that parsing a journal never panics.
//
// Journals are read after a failed apply, which is exactly when the file is
// most likely to be truncated or malformed — a crash there would obstruct
// recovery at the worst possible moment.
func FuzzReadJournal(f *testing.F) {
	f.Add(`{"type":"header","header":{"format_version":"sixsync.journal/v1alpha1",` +
		`"started_at":"2026-08-23T00:00:00Z","plan_digest":"sha256:x",` +
		`"admin_url":"http://gw.example.test:9180","operation_count":1}}` + "\n")
	f.Add("")
	f.Add("{}\n")
	f.Add("not json\n")
	f.Add(`{"type":"header","header":{"format_version":"sixsync.journal/v1alpha1"}}` + "\n" +
		`{"type":"result","result":{"kind":"Route","id":"r1","outcome":"applied"}}` + "\n")
	f.Add(`{"type":"unknown"}` + "\n")

	f.Fuzz(func(t *testing.T, data string) {
		j, err := Read(strings.NewReader(data))
		if err != nil {
			return
		}
		if j == nil {
			t.Fatal("Read returned no error and no journal")
		}

		// An accepted journal must have a header in the format this build
		// implements, or rollback could act on records it does not understand.
		if j.Header.FormatVersion != FormatVersion {
			t.Fatalf("Read accepted format %q", j.Header.FormatVersion)
		}

		// Looking up prior state must never panic, whatever the contents.
		for _, b := range j.Befores {
			if got := j.BeforeFor(b.Kind, b.ID); got == nil {
				t.Fatalf("a recorded prior state could not be looked up: %s/%s", b.Kind, b.ID)
			}
		}
		_ = j.Complete()
	})
}
