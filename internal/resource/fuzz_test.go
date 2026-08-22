package resource

import (
	"strings"
	"testing"
)

// FuzzDecode checks that parsing a resource file never panics and never
// accepts something Validate would reject.
//
// The parser is the outermost boundary of the tool: it reads files from a
// repository that many people can write to, so a panic here is a denial of
// service and a wrongly-accepted file is a configuration error that reaches a
// gateway.
func FuzzDecode(f *testing.F) {
	f.Add("apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r1\n" +
		"lifecycle:\n  state: present\nspec:\n  uri: /a\n")
	f.Add("apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n  id: u1\n" +
		"lifecycle:\n  state: present\nspec:\n  nodes:\n    10.0.0.1:80: 1\n")
	f.Add("apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r1\n" +
		"lifecycle:\n  state: absent\n")
	f.Add("")
	f.Add("{}")
	f.Add("[]")
	f.Add("&a [*a]")                    // a self-referential alias
	f.Add("a: &x\n  b: *x\n")           // recursive anchor
	f.Add(strings.Repeat("a:\n ", 200)) // deep nesting
	f.Add("apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r1\n  id: r2\n" +
		"lifecycle:\n  state: present\nspec:\n  uri: /a\n") // duplicate key

	f.Fuzz(func(t *testing.T, data string) {
		file, err := Decode([]byte(data))
		if err != nil {
			return
		}
		if file == nil {
			t.Fatal("Decode returned no error and no file")
		}

		// Anything Decode accepts must also satisfy Validate: Decode calls it,
		// so a divergence would mean an invalid file passed through.
		if err := file.Validate(); err != nil {
			t.Fatalf("Decode accepted a file that Validate rejects: %v", err)
		}

		// An accepted file must round-trip. Encode refuses invalid files, so a
		// failure here means Decode admitted something unrepresentable.
		encoded, err := Encode(file)
		if err != nil {
			t.Fatalf("a decoded file could not be re-encoded: %v", err)
		}
		if _, err := Decode(encoded); err != nil {
			t.Fatalf("a re-encoded file no longer parses: %v", err)
		}
	})
}

// FuzzValidateID checks the identifier rule against arbitrary input.
func FuzzValidateID(f *testing.F) {
	for _, seed := range []string{
		"r1", "", "a b", strings.Repeat("a", 65), "00000000000000123456",
		"../etc/passwd", "a/b", "café", "\x00", "-", ".",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, id string) {
		err := ValidateID(id)
		if err != nil {
			return
		}

		// An accepted identifier must satisfy APISIX's own rule, or SixSync
		// would write something the gateway rejects.
		if len(id) < 1 || len(id) > 64 {
			t.Fatalf("ValidateID accepted a %d-character identifier", len(id))
		}
		for _, r := range id {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
				r == '-', r == '_', r == '.':
			default:
				t.Fatalf("ValidateID accepted %q, which contains %q", id, r)
			}
		}

		// An accepted identifier must be safe in a file name: it becomes part
		// of a path, so a traversal sequence here would escape the repository.
		if strings.Contains(id, "/") || strings.Contains(id, `\`) || id == ".." {
			t.Fatalf("ValidateID accepted a path-unsafe identifier %q", id)
		}
	})
}

// FuzzFileName checks that a generated file name is always a single safe path
// segment, whatever the metadata contains.
func FuzzFileName(f *testing.F) {
	f.Add("payments-export", "r1")
	f.Add("../../etc/passwd", "r1")
	f.Add("", "")
	f.Add(strings.Repeat("x", 500), "r1")
	f.Add("a/b/c", "d/e/f")
	f.Add("\x00\x01", "r1")

	f.Fuzz(func(t *testing.T, name, id string) {
		file := &File{
			APIVersion: APIVersion, Kind: "Route",
			Metadata:  Metadata{ID: id, Name: name},
			Lifecycle: Lifecycle{State: StatePresent},
			Spec:      map[string]any{"uri": "/a"},
		}
		got := FileName(file)

		// The result is joined onto a directory, so it must never escape it.
		for _, bad := range []string{"/", `\`, "..", "\x00"} {
			if strings.Contains(got, bad) {
				t.Fatalf("FileName(%q, %q) = %q, which contains %q and could escape the "+
					"repository directory", name, id, got, bad)
			}
		}
		if !strings.HasSuffix(got, ".yaml") {
			t.Fatalf("FileName(%q, %q) = %q, which is not a YAML file", name, id, got)
		}
		if got == ".yaml" {
			t.Fatalf("FileName(%q, %q) produced a name with no stem", name, id)
		}
		// A file name that is too long fails to write on most filesystems.
		if len(got) > 255 {
			t.Fatalf("FileName(%q, %q) is %d characters, which most filesystems reject",
				name, id, len(got))
		}
	})
}
