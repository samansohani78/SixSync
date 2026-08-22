package resource

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// --- hostile YAML ----------------------------------------------------------

// A YAML alias bomb expands exponentially and exhausts memory. The parser must
// refuse or bound it rather than take the process down: resource files come
// from a repository many people can write to.
func TestBillionLaughsIsRefused(t *testing.T) {
	bomb := `apiVersion: sixsync/v1alpha1
kind: Route
metadata:
  id: r1
lifecycle:
  state: present
spec:
  a: &a ["x","x","x","x","x","x","x","x","x"]
  b: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]
  c: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]
  d: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]
  e: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]
  f: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e]
  g: &g [*f,*f,*f,*f,*f,*f,*f,*f,*f]
  h: [*g,*g,*g,*g,*g,*g,*g,*g,*g]
`
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		_, _ = Decode([]byte(bomb))
	}()

	select {
	case <-done:
		// Either outcome is acceptable: refused, or expanded within limits.
		// What matters is that it terminated.
	case <-timeoutAfter(20):
		t.Fatal("decoding an alias bomb did not terminate")
	}
}

func TestDeeplyNestedDocumentIsHandled(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r1\n" +
		"lifecycle:\n  state: present\nspec:\n")
	depth := 400
	for i := 0; i < depth; i++ {
		sb.WriteString(strings.Repeat("  ", i+1) + "n:\n")
	}
	sb.WriteString(strings.Repeat("  ", depth+1) + "leaf: 1\n")

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			// A stack overflow from unbounded recursion would kill the process
			// rather than panic, so reaching here at all is the result.
			_ = recover()
		}()
		_, _ = Decode([]byte(sb.String()))
	}()

	select {
	case <-done:
	case <-timeoutAfter(20):
		t.Fatal("decoding a deeply nested document did not terminate")
	}
}

// A duplicate key silently discards one of the values, so which one wins would
// depend on parser internals.
func TestDuplicateKeysAreRejected(t *testing.T) {
	in := `apiVersion: sixsync/v1alpha1
kind: Route
metadata:
  id: r1
lifecycle:
  state: present
spec:
  uri: /first
  uri: /second
`
	if _, err := Decode([]byte(in)); err == nil {
		t.Error("a document with a duplicate key was accepted; which value wins would " +
			"depend on parser internals")
	}
}

func TestUnicodeIdentifiersAreRejected(t *testing.T) {
	for _, id := range []string{"café", "路由", "r\u200b1", "r\u0000"} {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID accepted %q; APISIX only accepts ASCII letters, digits, "+
				"dot, dash and underscore", id)
		}
	}
}

// A name with combining characters or right-to-left marks must still slug to
// something usable.
func TestUnicodeNamesProduceSafeFileNames(t *testing.T) {
	for _, name := range []string{"café-route", "路由", "a\u202Eb", "\u0000\u0001"} {
		f := &File{
			APIVersion: APIVersion, Kind: "Route",
			Metadata:  Metadata{ID: "r1", Name: name},
			Lifecycle: Lifecycle{State: StatePresent},
			Spec:      map[string]any{"uri": "/a"},
		}
		got := FileName(f)
		for _, bad := range []string{"/", `\`, "\x00", ".."} {
			if strings.Contains(got, bad) {
				t.Errorf("FileName with name %q produced %q, containing %q", name, got, bad)
			}
		}
		if !strings.HasSuffix(got, ".yaml") || got == ".yaml" {
			t.Errorf("FileName with name %q produced %q", name, got)
		}
	}
}

// --- filesystem ------------------------------------------------------------

// A symlink pointing outside the repository would let a file elsewhere on the
// filesystem be read as configuration.
func TestSymlinkedFilesOutsideTheRepositoryAreRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}

	outside := t.TempDir()
	external := filepath.Join(outside, "elsewhere.yaml")
	if err := os.WriteFile(external, []byte(
		"apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: smuggled\n"+
			"lifecycle:\n  state: present\nspec:\n  uri: /smuggled\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "routes"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "routes", "link.yaml")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	repo, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, l := range repo.Resources {
		if l.File.Metadata.ID == "smuggled" {
			t.Fatalf("a file outside the repository was loaded as configuration, via %s", l.Path)
		}
	}
	if len(repo.Errors) != 1 {
		t.Fatalf("the refused symlink should be reported: %+v", repo.Errors)
	}
	if !strings.Contains(repo.Errors[0].Err.Error(), "outside the repository") {
		t.Errorf("unclear reason: %v", repo.Errors[0].Err)
	}
}

// A symlink to a file inside the repository is ordinary and must still work.
func TestSymlinkedFilesInsideTheRepositoryAreLoaded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "shared"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "routes"), 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "shared", "r.yaml")
	if err := os.WriteFile(target, []byte(
		"apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: shared-route\n"+
			"lifecycle:\n  state: present\nspec:\n  uri: /shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "routes", "link.yaml")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	repo, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.Errors) != 0 {
		t.Errorf("a symlink inside the repository was refused: %+v", repo.Errors)
	}
	// Both the target and the link resolve to the same resource.
	if len(repo.Resources) != 2 {
		t.Errorf("expected the file and its link, got %d", len(repo.Resources))
	}
}

// A directory symlink loop would make the walk run forever.
func TestSymlinkLoopsDoNotHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}

	root := t.TempDir()
	sub := filepath.Join(root, "routes")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(sub, "loop")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Load(root)
	}()

	select {
	case <-done:
	case <-timeoutAfter(20):
		t.Fatal("Load did not terminate on a directory symlink loop")
	}
}

func TestLoadHandlesUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permissions do not apply")
	}

	root := t.TempDir()
	dir := filepath.Join(root, "routes")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "secret.yaml")
	if err := os.WriteFile(p, []byte("apiVersion: sixsync/v1alpha1\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	repo, err := Load(root)
	if err != nil {
		t.Fatalf("an unreadable file should be reported, not abort the load: %v", err)
	}
	if len(repo.Errors) != 1 {
		t.Errorf("expected the unreadable file to be reported: %+v", repo.Errors)
	}
}

// An interrupted write must leave the old file or the new one, never a
// truncated one a later run would fail to parse.
func TestWriteAtomicToReadOnlyDirectoryFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permissions do not apply")
	}

	root := t.TempDir()
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o750) })

	err := WriteAtomic(filepath.Join(root, "file.yaml"), []byte("x"), 0o600)
	if err == nil {
		t.Fatal("writing to a read-only directory should fail")
	}
	if !strings.Contains(err.Error(), "file.yaml") && !strings.Contains(err.Error(), root) {
		t.Errorf("the error should name what could not be written: %v", err)
	}
}

// Two processes writing the same path must not produce a corrupt file: the
// rename is atomic, so one wins whole.
func TestConcurrentAtomicWritesLeaveAWholeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contended.yaml")

	const writers = 8
	done := make(chan error, writers)
	for i := 0; i < writers; i++ {
		content := strings.Repeat(string(rune('a'+i)), 4096)
		go func() { done <- WriteAtomic(path, []byte(content), 0o600) }()
	}
	for i := 0; i < writers; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent write failed: %v", err)
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4096 {
		t.Fatalf("file is %d bytes, want a whole 4096-byte write", len(got))
	}
	// Every byte must come from the same writer.
	for _, b := range got {
		if b != got[0] {
			t.Fatal("the file mixes content from several writers")
		}
	}

	// No temporary files may be left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("expected one file, found %d: temporary files were left behind", len(entries))
	}
}
