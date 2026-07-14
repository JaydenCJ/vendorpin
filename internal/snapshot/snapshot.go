// Package snapshot models a vendored file tree as an immutable set of
// relative paths with content and an executable bit. It computes the
// per-file and whole-tree SHA-256 digests recorded in the lockfile, and it
// is the only package that reads or writes vendored files on disk. All
// digest logic is pure so it can be tested byte-for-byte.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// File modes recorded in the lockfile. vendorpin tracks exactly one
// permission signal — the executable bit — because that is the only mode
// information git itself preserves.
const (
	ModeRegular = "644"
	ModeExec    = "755"
)

// File is one regular file inside a snapshot.
type File struct {
	Path string // slash-separated path relative to the tree root
	Mode string // ModeRegular or ModeExec
	Data []byte
}

// Snapshot is a set of files keyed by relative path.
type Snapshot struct {
	files map[string]File
}

// New returns an empty snapshot.
func New() *Snapshot {
	return &Snapshot{files: make(map[string]File)}
}

// ValidatePath rejects anything that could escape the destination tree or
// behave differently across machines: absolute paths, `.`/`..` segments,
// backslashes, empty segments, and non-canonical forms. Vendoring untrusted
// upstreams is the whole point, so this is deliberately strict.
func ValidatePath(p string) error {
	if p == "" {
		return fmt.Errorf("empty path")
	}
	if strings.ContainsRune(p, '\x00') {
		return fmt.Errorf("path %q contains a NUL byte", p)
	}
	if strings.Contains(p, `\`) {
		return fmt.Errorf("path %q contains a backslash; snapshot paths are slash-separated", p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("path %q is absolute; snapshot paths must be relative", p)
	}
	if path.Clean(p) != p {
		return fmt.Errorf("path %q is not canonical (want %q)", p, path.Clean(p))
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("path %q contains a %q segment", p, part)
		}
	}
	return nil
}

// Add inserts a file, rejecting invalid paths, unknown modes, and
// duplicates.
func (s *Snapshot) Add(p, mode string, data []byte) error {
	if err := ValidatePath(p); err != nil {
		return err
	}
	if mode != ModeRegular && mode != ModeExec {
		return fmt.Errorf("path %q: unknown mode %q (want %s or %s)", p, mode, ModeRegular, ModeExec)
	}
	if _, ok := s.files[p]; ok {
		return fmt.Errorf("duplicate path %q", p)
	}
	s.files[p] = File{Path: p, Mode: mode, Data: data}
	return nil
}

// Len reports the number of files.
func (s *Snapshot) Len() int { return len(s.files) }

// Paths returns every file path in sorted order.
func (s *Snapshot) Paths() []string {
	out := make([]string, 0, len(s.files))
	for p := range s.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Get looks up one file by path.
func (s *Snapshot) Get(p string) (File, bool) {
	f, ok := s.files[p]
	return f, ok
}

// FileDigest returns the lockfile digest string for file content:
// "sha256:" followed by the lowercase hex SHA-256 of the bytes.
func FileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TreeDigest hashes the whole snapshot into one digest. The pre-image is
// one line per file in sorted path order — "<mode> <filedigest> <path>\n" —
// so any content, mode, rename, addition, or removal changes it. The exact
// format is documented in docs/lockfile-format.md and must never change
// within a lockfile version.
func (s *Snapshot) TreeDigest() string {
	h := sha256.New()
	for _, p := range s.Paths() {
		f := s.files[p]
		fmt.Fprintf(h, "%s %s %s\n", f.Mode, FileDigest(f.Data), f.Path)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// modeOf maps an on-disk permission to the lockfile mode string.
func modeOf(perm fs.FileMode) string {
	if perm&0o111 != 0 {
		return ModeExec
	}
	return ModeRegular
}

// ReadDir loads the tree rooted at root into a snapshot. It fails on
// anything that is not a regular file (symbolic links included) because a
// digest over a link target would be meaningless and a link is itself a
// form of drift worth surfacing loudly. A missing root returns an error
// satisfying errors.Is(err, fs.ErrNotExist).
func ReadDir(root string) (*Snapshot, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	s := New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: unsupported file type (symbolic links are not supported)", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return s.Add(rel, modeOf(info.Mode().Perm()), data)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// WriteDir materializes the snapshot under root, creating directories as
// needed. Existing files are replaced (never chmod-ed in place) so the
// resulting mode always matches the snapshot exactly.
func (s *Snapshot) WriteDir(root string) error {
	for _, p := range s.Paths() {
		f := s.files[p]
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
		perm := fs.FileMode(0o644)
		if f.Mode == ModeExec {
			perm = 0o755
		}
		if err := os.WriteFile(abs, f.Data, perm); err != nil {
			return err
		}
	}
	return nil
}

// RemoveTracked deletes the given tracked paths under root, then prunes any
// directories left empty — including root itself. Files not listed (local
// extras) are deliberately left alone; callers surface them to the user.
// It returns how many files were actually deleted.
func RemoveTracked(root string, paths []string) (int, error) {
	removed := 0
	for _, p := range paths {
		if err := ValidatePath(p); err != nil {
			return removed, err
		}
		abs := filepath.Join(root, filepath.FromSlash(p))
		err := os.Remove(abs)
		switch {
		case err == nil:
			removed++
		case os.IsNotExist(err):
			// Already gone: nothing to do, not an error.
		default:
			return removed, err
		}
	}
	if err := pruneEmptyDirs(root); err != nil {
		return removed, err
	}
	return removed, nil
}

// pruneEmptyDirs removes dir if it is (recursively) empty. A missing dir is
// fine — there is nothing to prune.
func pruneEmptyDirs(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			if err := pruneEmptyDirs(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return os.Remove(dir)
	}
	return nil
}
