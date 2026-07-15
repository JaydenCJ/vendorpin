// Package gitio talks to the local git binary: clone an upstream into a
// temporary bare repository, resolve refs to commits, and read a (sub)tree
// at a pinned commit via `git archive` parsed with archive/tar — no
// worktree checkout ever touches disk. Everything downstream of the tar
// stream is pure and tested against real repositories built in temp dirs.
package gitio

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/JaydenCJ/vendorpin/internal/snapshot"
)

// run executes git with hardened per-command config so user configuration
// (pagers, signature display) cannot change the output shape, and with
// terminal prompts disabled so a missing credential fails fast instead of
// hanging.
func run(dir string, args ...string) ([]byte, error) {
	full := append([]string{
		"-c", "core.pager=cat",
		"-c", "log.showSignature=false",
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("git: executable not found in PATH")
		}
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], firstLine(msg))
	}
	return out.Bytes(), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// CloneBare clones upstream into dir as a bare repository. Bare is enough:
// vendorpin only ever reads objects, never checks a worktree out.
func CloneBare(upstream, dir string) error {
	_, err := run("", "clone", "--quiet", "--bare", "--", upstream, dir)
	if err != nil {
		return fmt.Errorf("cannot reach upstream %q: %v", upstream, err)
	}
	return nil
}

// ResolveCommit resolves a ref (branch, tag, or commit hash) inside the
// bare clone at gitDir to a full 40-hex commit hash.
func ResolveCommit(gitDir, ref string) (string, error) {
	out, err := run(gitDir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("ref %q not found in upstream", ref)
	}
	commit := strings.TrimSpace(string(out))
	if len(commit) != 40 {
		return "", fmt.Errorf("ref %q resolved to unexpected %q", ref, commit)
	}
	return commit, nil
}

// CommitTime returns the author date of a commit in strict ISO-8601 form
// (git's %aI), normalized to a numeric offset for lockfile stability.
func CommitTime(gitDir, commit string) (string, error) {
	out, err := run(gitDir, "show", "-s", "--format=%aI", commit)
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(string(out))
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", fmt.Errorf("parse commit time %q: %v", raw, err)
	}
	return ts.Format("2006-01-02T15:04:05-07:00"), nil
}

// Archive reads the tree at commit (restricted to subdir when non-empty)
// into a snapshot, with subdir stripped from every path. It refuses
// symbolic and hard links outright — a vendored link is a path-traversal
// hazard — and rejects any entry whose path would escape the destination.
func Archive(gitDir, commit, subdir string) (*snapshot.Snapshot, error) {
	args := []string{"archive", "--format=tar", commit}
	if subdir != "" {
		if err := snapshot.ValidatePath(subdir); err != nil {
			return nil, fmt.Errorf("upstream path: %v", err)
		}
		if strings.HasPrefix(subdir, "-") {
			return nil, fmt.Errorf("upstream path %q must not start with '-'", subdir)
		}
		args = append(args, subdir)
	}
	out, err := run(gitDir, args...)
	if err != nil {
		if strings.Contains(err.Error(), "did not match any files") {
			return nil, fmt.Errorf("path %q not found at commit %s", subdir, commit[:7])
		}
		return nil, err
	}
	snap, err := parseTar(out, subdir)
	if err != nil {
		return nil, err
	}
	if snap.Len() == 0 {
		where := "the repository root"
		if subdir != "" {
			where = fmt.Sprintf("%q", subdir)
		}
		return nil, fmt.Errorf("no files under %s at commit %s", where, commit[:7])
	}
	return snap, nil
}

// parseTar turns a `git archive` tar stream into a snapshot, stripping the
// subdir prefix from entry names.
func parseTar(data []byte, subdir string) (*snapshot.Snapshot, error) {
	snap := snapshot.New()
	prefix := ""
	if subdir != "" {
		prefix = subdir + "/"
	}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading archive: %v", err)
		}
		switch hdr.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeDir:
			continue
		case tar.TypeSymlink, tar.TypeLink:
			return nil, fmt.Errorf("upstream entry %q is a link; links are not supported in v0.1.0", strings.TrimSuffix(hdr.Name, "/"))
		case tar.TypeReg:
			// The one entry type vendorpin materializes.
		default:
			return nil, fmt.Errorf("upstream entry %q has unsupported type %q", hdr.Name, hdr.Typeflag)
		}
		name := path.Clean(hdr.Name)
		if prefix != "" {
			if name == subdir {
				return nil, fmt.Errorf("upstream path %q is a file, not a directory", subdir)
			}
			if !strings.HasPrefix(name, prefix) {
				continue // defensive: git archive should only emit matches
			}
			name = strings.TrimPrefix(name, prefix)
		}
		mode := snapshot.ModeRegular
		if hdr.FileInfo().Mode().Perm()&0o111 != 0 {
			mode = snapshot.ModeExec
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading %q from archive: %v", hdr.Name, err)
		}
		if err := snap.Add(name, mode, content); err != nil {
			return nil, fmt.Errorf("archive entry rejected: %v", err)
		}
	}
	return snap, nil
}
