package resource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/model"
)

func seed(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const routeA = `apiVersion: sixsync/v1alpha1
kind: Route
metadata:
  id: route-a
lifecycle:
  state: present
spec:
  uri: /a
`

func TestLoadFindsResourcesRecursively(t *testing.T) {
	root := seed(t, map[string]string{
		"routes/a.yaml":       routeA,
		"nested/deeper/b.yml": strings.Replace(routeA, "route-a", "route-b", 1),
		"notes.md":            "not a resource",
		"routes/README.txt":   "also not a resource",
	})

	repo, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(repo.Resources) != 2 {
		t.Errorf("loaded %d resources, want 2", len(repo.Resources))
	}
	if len(repo.Errors) != 0 {
		t.Errorf("unexpected load errors: %v", repo.Errors)
	}
}

// .git and editor state must never be mistaken for configuration.
func TestLoadSkipsHiddenDirectoriesAndFiles(t *testing.T) {
	root := seed(t, map[string]string{
		"routes/a.yaml":    routeA,
		".git/config.yaml": routeA,
		".hidden/b.yaml":   routeA,
		"routes/.swp.yaml": routeA,
	})

	repo, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(repo.Resources) != 1 {
		t.Errorf("loaded %d resources, want 1; hidden paths must be skipped", len(repo.Resources))
	}
}

// One run should report everything wrong with a repository, not one thing at
// a time.
func TestLoadCollectsErrorsInsteadOfStopping(t *testing.T) {
	root := seed(t, map[string]string{
		"routes/good.yaml": routeA,
		"routes/bad1.yaml": "kind: Route\n  broken: [",
		"routes/bad2.yaml": "apiVersion: wrong/v1\nkind: Route\nmetadata:\n  id: x\nlifecycle:\n  state: present\nspec:\n  uri: /x\n",
	})

	repo, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(repo.Resources) != 1 {
		t.Errorf("the good file should still load, got %d", len(repo.Resources))
	}
	if len(repo.Errors) != 2 {
		t.Errorf("expected both bad files reported, got %d: %v", len(repo.Errors), repo.Errors)
	}
	for _, e := range repo.Errors {
		if !strings.Contains(e.Error(), ".yaml") {
			t.Errorf("a load error should name its file: %s", e)
		}
	}
}

func TestLoadOrderingIsDeterministic(t *testing.T) {
	files := map[string]string{}
	for _, id := range []string{"zebra", "alpha", "middle"} {
		files["routes/"+id+".yaml"] = strings.Replace(routeA, "route-a", id, 1)
	}
	root := seed(t, files)

	var first []string
	for run := 0; run < 20; run++ {
		repo, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, l := range repo.Resources {
			got = append(got, l.File.Metadata.ID)
		}
		if run == 0 {
			first = got
			continue
		}
		if strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("load order varied: %v then %v", first, got)
		}
	}
	if strings.Join(first, ",") != "alpha,middle,zebra" {
		t.Errorf("resources are not sorted by id: %v", first)
	}
}

func TestByKey(t *testing.T) {
	root := seed(t, map[string]string{"routes/a.yaml": routeA})
	repo, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	idx := repo.ByKey()
	if _, ok := idx[Key{Kind: model.KindRoute, ID: "route-a"}]; !ok {
		t.Errorf("resource not indexed: %+v", idx)
	}
}

func TestLoadRejectsANonDirectory(t *testing.T) {
	root := seed(t, map[string]string{"routes/a.yaml": routeA})
	if _, err := Load(filepath.Join(root, "routes/a.yaml")); err == nil {
		t.Error("expected an error when pointed at a file")
	}
	if _, err := Load(filepath.Join(root, "does-not-exist")); err == nil {
		t.Error("expected an error for a missing directory")
	}
}

// An interrupted import must leave the old file or the new one, never a
// truncated one a later run would fail to parse.
func TestWriteAtomicReplacesCleanly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "file.yaml")

	if err := WriteAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" {
		t.Errorf("content = %q, want %q", got, "first")
	}

	if err := WriteAtomic(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("WriteAtomic (replace): %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "second" {
		t.Errorf("content = %q, want %q", got, "second")
	}
}

func TestWriteAtomicSetsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.yaml")
	if err := WriteAtomic(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// An imported file describes a production gateway's routing.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600", perm)
	}
}

// A failed write must not leave temporary files behind for the loader to trip
// over on the next run.
func TestWriteAtomicLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAtomic(filepath.Join(dir, "file.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), "tmp") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("expected exactly one file, got %d", len(entries))
	}
}
