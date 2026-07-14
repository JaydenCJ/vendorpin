// Package lockfile reads, validates, and writes vendorpin.lock — the
// provenance record for every vendored tree. The file is deliberately
// boring JSON: sorted entries, stable indentation, full 40-hex commits, and
// digests prefixed with their algorithm, so diffs of the lockfile itself
// are reviewable and any hand edit that breaks an invariant is rejected
// loudly on load. See docs/lockfile-format.md for the format contract.
package lockfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/JaydenCJ/vendorpin/internal/snapshot"
	"github.com/JaydenCJ/vendorpin/internal/version"
)

// Filename is the default lockfile name, looked up in the working
// directory unless --lock points elsewhere.
const Filename = "vendorpin.lock"

// FormatVersion is the current lockfile schema version. Loading any other
// version fails: silently reinterpreting a provenance record would defeat
// its purpose.
const FormatVersion = 1

// FileRecord pins one file: its path relative to the entry's dest, its
// content digest, and its executable bit.
type FileRecord struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Mode   string `json:"mode"`
}

// Entry pins one vendored tree.
type Entry struct {
	Name       string       `json:"name"`
	Upstream   string       `json:"upstream"`
	Ref        string       `json:"ref"`
	Commit     string       `json:"commit"`
	CommitTime string       `json:"commit_time"`
	Path       string       `json:"path,omitempty"` // subdirectory in the upstream; "" = repo root
	Dest       string       `json:"dest"`
	Tree       string       `json:"tree"`
	Files      []FileRecord `json:"files"`
}

// Lockfile is the top-level document.
type Lockfile struct {
	Version     int     `json:"lockfile_version"`
	GeneratedBy string  `json:"generated_by"`
	Vendors     []Entry `json:"vendors"`
}

// New returns an empty lockfile stamped with the current tool version.
func New() *Lockfile {
	return &Lockfile{
		Version:     FormatVersion,
		GeneratedBy: "vendorpin " + version.Version,
		Vendors:     []Entry{},
	}
}

var (
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ValidateName enforces the vendor-name charset: it doubles as a directory
// name and a CLI argument, so it stays conservative.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("vendor name is empty")
	}
	if len(name) > 100 {
		return fmt.Errorf("vendor name %q is longer than 100 characters", name)
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("vendor name %q: only letters, digits, '.', '_' and '-' are allowed, and it must not start with a punctuation character", name)
	}
	return nil
}

// NameFromUpstream derives a default vendor name from an upstream URL or
// local path: the last path segment with any ".git" suffix stripped.
// It understands https://, ssh://, scp-like (git@host:owner/repo.git), and
// plain filesystem paths.
func NameFromUpstream(upstream string) (string, error) {
	s := strings.TrimRight(upstream, "/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	if err := ValidateName(s); err != nil {
		return "", fmt.Errorf("cannot derive a vendor name from %q: %v (pass --name)", upstream, err)
	}
	return s, nil
}

// RecordsFromSnapshot converts a snapshot into sorted lockfile records.
func RecordsFromSnapshot(s *snapshot.Snapshot) []FileRecord {
	out := make([]FileRecord, 0, s.Len())
	for _, p := range s.Paths() {
		f, _ := s.Get(p)
		out = append(out, FileRecord{Path: p, Digest: snapshot.FileDigest(f.Data), Mode: f.Mode})
	}
	return out
}

// validate checks every invariant the rest of the tool relies on. Errors
// name the offending entry so a hand-edited lockfile is easy to fix.
func (l *Lockfile) validate() error {
	if l.Version != FormatVersion {
		return fmt.Errorf("unsupported lockfile_version %d (this vendorpin understands version %d)", l.Version, FormatVersion)
	}
	seenNames := make(map[string]bool)
	for i := range l.Vendors {
		e := &l.Vendors[i]
		if err := ValidateName(e.Name); err != nil {
			return fmt.Errorf("vendors[%d]: %v", i, err)
		}
		if seenNames[e.Name] {
			return fmt.Errorf("duplicate vendor name %q", e.Name)
		}
		seenNames[e.Name] = true
		if e.Upstream == "" {
			return fmt.Errorf("vendor %q: upstream is empty", e.Name)
		}
		if !commitRe.MatchString(e.Commit) {
			return fmt.Errorf("vendor %q: commit %q is not a 40-hex commit hash", e.Name, e.Commit)
		}
		if err := snapshot.ValidatePath(e.Dest); err != nil {
			return fmt.Errorf("vendor %q: dest: %v", e.Name, err)
		}
		if e.Path != "" {
			if err := snapshot.ValidatePath(e.Path); err != nil {
				return fmt.Errorf("vendor %q: path: %v", e.Name, err)
			}
		}
		if !digestRe.MatchString(e.Tree) {
			return fmt.Errorf("vendor %q: tree digest %q is malformed", e.Name, e.Tree)
		}
		seenPaths := make(map[string]bool)
		for _, f := range e.Files {
			if err := snapshot.ValidatePath(f.Path); err != nil {
				return fmt.Errorf("vendor %q: file: %v", e.Name, err)
			}
			if seenPaths[f.Path] {
				return fmt.Errorf("vendor %q: duplicate file path %q", e.Name, f.Path)
			}
			seenPaths[f.Path] = true
			if !digestRe.MatchString(f.Digest) {
				return fmt.Errorf("vendor %q: file %q: digest %q is malformed", e.Name, f.Path, f.Digest)
			}
			if f.Mode != snapshot.ModeRegular && f.Mode != snapshot.ModeExec {
				return fmt.Errorf("vendor %q: file %q: mode %q is not %s or %s", e.Name, f.Path, f.Mode, snapshot.ModeRegular, snapshot.ModeExec)
			}
		}
	}
	return nil
}

// Load reads and strictly validates a lockfile. A missing file returns an
// error satisfying os.IsNotExist so callers can give a targeted hint.
func Load(path string) (*Lockfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Lockfile
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("%s: not a valid lockfile: %v", path, err)
	}
	if err := l.validate(); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if l.Vendors == nil {
		l.Vendors = []Entry{}
	}
	return &l, nil
}

// Save writes the lockfile with sorted entries, two-space indentation, and
// a trailing newline, via a temp file + rename so a crash can never leave a
// half-written lockfile behind. The generated_by stamp is refreshed to the
// running tool version.
func (l *Lockfile) Save(path string) error {
	l.Version = FormatVersion
	l.GeneratedBy = "vendorpin " + version.Version
	sort.Slice(l.Vendors, func(i, j int) bool { return l.Vendors[i].Name < l.Vendors[j].Name })
	if l.Vendors == nil {
		l.Vendors = []Entry{}
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Find returns the index of the named entry, or -1.
func (l *Lockfile) Find(name string) int {
	for i := range l.Vendors {
		if l.Vendors[i].Name == name {
			return i
		}
	}
	return -1
}

// SetEntry inserts or replaces the entry with the same name.
func (l *Lockfile) SetEntry(e Entry) {
	if i := l.Find(e.Name); i >= 0 {
		l.Vendors[i] = e
		return
	}
	l.Vendors = append(l.Vendors, e)
}

// RemoveEntry deletes the named entry, reporting whether it existed.
func (l *Lockfile) RemoveEntry(name string) bool {
	i := l.Find(name)
	if i < 0 {
		return false
	}
	l.Vendors = append(l.Vendors[:i], l.Vendors[i+1:]...)
	return true
}

// Dir returns the directory a lockfile path lives in; every dest in the
// lockfile is resolved relative to it.
func Dir(lockPath string) string {
	return filepath.Dir(lockPath)
}
