package plan

import (
	"strings"
	"testing"
)

// FuzzReadPlan checks that parsing a plan artifact never panics and never
// accepts one whose digest does not match.
//
// A plan is read from a CI artifact store, so it is attacker-adjacent input:
// anyone who can write to that store could otherwise steer an apply.
func FuzzReadPlan(f *testing.F) {
	valid := &Plan{Content: Content{
		FormatVersion: FormatVersion,
		Target:        Target{AdminURL: "http://gw.example.test:9180"},
		Operations: []Operation{{
			Type: OpUpdate, Status: StatusPlanned, Kind: "Route", ID: "r1",
			Precondition: Precondition{ObservedModifiedIndex: 42},
		}},
	}}
	if err := valid.Seal(); err != nil {
		f.Fatal(err)
	}
	var sb strings.Builder
	if err := Write(&sb, valid); err != nil {
		f.Fatal(err)
	}

	f.Add(sb.String())
	f.Add("{}")
	f.Add("")
	f.Add("null")
	f.Add(`{"content":{"format_version":"sixsync.plan/v1alpha1"},"digest":"sha256:0"}`)

	f.Fuzz(func(t *testing.T, data string) {
		p, err := Read(strings.NewReader(data))
		if err != nil {
			return
		}
		if p == nil {
			t.Fatal("Read returned no error and no plan")
		}

		// Anything Read accepts must verify: an accepted plan whose digest
		// does not match its content would let a modified artifact be applied.
		if err := p.Verify(); err != nil {
			t.Fatalf("Read accepted a plan that does not verify: %v", err)
		}
		if p.Content.FormatVersion != FormatVersion {
			t.Fatalf("Read accepted format %q", p.Content.FormatVersion)
		}

		// It must re-serialize and re-read to the same digest.
		var out strings.Builder
		if err := Write(&out, p); err != nil {
			t.Fatalf("an accepted plan could not be re-written: %v", err)
		}
		again, err := Read(strings.NewReader(out.String()))
		if err != nil {
			t.Fatalf("a re-written plan no longer parses: %v", err)
		}
		if again.Digest != p.Digest {
			t.Fatalf("digest changed across a round trip: %s then %s", p.Digest, again.Digest)
		}
	})
}
