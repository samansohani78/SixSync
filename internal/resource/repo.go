package resource

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/samansohani78/SixSync/internal/model"
)

// Loaded is one resource file together with where it came from, so that every
// diagnostic can name a path the operator can open.
type Loaded struct {
	Path string
	File *File
}

// LoadError is a problem with one file. Loading collects these rather than
// stopping at the first, so a single run reports everything wrong with a
// repository instead of one thing per invocation.
type LoadError struct {
	Path string
	Err  error
}

func (e LoadError) Error() string { return e.Path + ": " + e.Err.Error() }
func (e LoadError) Unwrap() error { return e.Err }

// Key identifies a resource uniquely within a repository.
type Key struct {
	Kind model.Kind
	ID   string
}

func (k Key) String() string { return string(k.Kind) + "/" + k.ID }

// Repo is a loaded set of resource files.
type Repo struct {
	// Root is the directory the files were loaded from.
	Root string

	// Resources are the successfully parsed files, ordered deterministically
	// by kind then ID so that every command's output is reproducible.
	Resources []Loaded

	// Errors are files that could not be loaded.
	Errors []LoadError
}

// ByKey indexes the loaded resources.
//
// When two files claim the same identity the first in sorted order wins here;
// the duplicate is reported separately by lint, which is where that belongs.
func (r *Repo) ByKey() map[Key]Loaded {
	out := make(map[Key]Loaded, len(r.Resources))
	for _, l := range r.Resources {
		k := Key{Kind: l.File.Kind, ID: l.File.Metadata.ID}
		if _, exists := out[k]; !exists {
			out[k] = l
		}
	}
	return out
}

// Load reads every resource file under root.
//
// Only .yaml and .yml files are considered, and hidden directories are skipped
// so that .git and editor state cannot be mistaken for configuration.
func Load(root string) (*Repo, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("reading repository directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}

	// Resolve the root once, so a repository reached through a symlink is
	// compared against its real location rather than reported as escaping
	// itself.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolving repository directory: %w", err)
	}

	repo := &Repo{Root: root}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		name := d.Name()
		if d.IsDir() {
			// Skip dot directories, but never the root itself even if the
			// user pointed at one (e.g. `sixsync lint --repo-dir .config`).
			if path != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}

		// The path comes from walking the directory the operator named. Reading
		// operator-specified files is precisely this function's purpose.
		// A symlink resolving outside the repository would let a file elsewhere
		// on the filesystem be read as configuration. Anyone able to commit a
		// symlink could then pull, say, a secrets file into a plan artifact —
		// and plan artifacts are attached to pipeline runs. The same person
		// could commit the content directly, so this is not a privilege
		// boundary; it stops an unintended read of something that was never
		// meant to be part of the repository.
		if outside, checkErr := escapesRoot(realRoot, path); checkErr != nil {
			repo.Errors = append(repo.Errors, LoadError{Path: path, Err: checkErr})
			return nil
		} else if outside {
			repo.Errors = append(repo.Errors, LoadError{
				Path: path,
				Err: errors.New("resolves outside the repository directory; SixSync does not " +
					"read files from outside the tree it was pointed at"),
			})
			return nil
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // G304: intentional
		if readErr != nil {
			repo.Errors = append(repo.Errors, LoadError{Path: path, Err: readErr})
			return nil
		}

		f, decErr := Decode(data)
		if decErr != nil {
			repo.Errors = append(repo.Errors, LoadError{Path: path, Err: decErr})
			return nil
		}

		repo.Resources = append(repo.Resources, Loaded{Path: path, File: f})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("scanning repository: %w", walkErr)
	}

	sort.Slice(repo.Resources, func(i, j int) bool {
		a, b := repo.Resources[i].File, repo.Resources[j].File
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Metadata.ID != b.Metadata.ID {
			return a.Metadata.ID < b.Metadata.ID
		}
		return repo.Resources[i].Path < repo.Resources[j].Path
	})
	sort.Slice(repo.Errors, func(i, j int) bool { return repo.Errors[i].Path < repo.Errors[j].Path })

	return repo, nil
}

// WriteAtomic writes data to path without ever leaving a partial file behind.
//
// The write goes to a temporary file in the same directory and is then renamed
// over the target, which is atomic on a POSIX filesystem. An interrupted
// `import` therefore leaves either the old file or the new one, never a
// truncated one that a later run would fail to parse.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	// 0750, matching the 0600 files inside: a resource tree describes a
	// production gateway's routing and does not need to be world-readable.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Clean up the temporary file on any failure path.
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck,gosec // the write error is what matters
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close() //nolint:errcheck,gosec
		return fmt.Errorf("setting permissions on %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}

	tmpName = "" // renamed successfully; nothing to clean up
	return nil
}

// escapesRoot reports whether a path resolves outside the repository root.
//
// Symlinks are resolved first, so a link pointing elsewhere is caught rather
// than the link's own location being checked.
func escapesRoot(realRoot, path string) (bool, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// A broken symlink resolves to nothing; "unreadable" describes that
		// more accurately than "escapes".
		return false, fmt.Errorf("cannot resolve: %w", err)
	}
	rel, err := filepath.Rel(realRoot, resolved)
	if err != nil {
		return true, nil
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}
