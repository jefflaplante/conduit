// Package sandbox implements the single file-path containment check shared by
// every tool that accepts a filesystem path (Read, Write, Edit, Glob, Bash cwd).
//
// conduit-31jg.6: previously there were two divergent checkers
// (Registry.isPathAllowed and EditTool.isPathAllowed) that compared lexical
// paths only, so a symlink inside the workspace pointing at /etc passed, and a
// `strings.HasPrefix(rel, "..")` test wrongly rejected names like "..notes".
//
// The unified rule:
//
//   - Roots are tools.sandbox.allowed_paths ∪ {tools.sandbox.workspace_dir}
//     (empty entries ignored). Relative roots are made absolute against the
//     process working directory, then canonicalized (symlinks resolved).
//   - A path is canonicalized the same way: made absolute and cleaned, then
//     symlinks are resolved on the deepest existing ancestor; components that do
//     not exist yet are appended lexically. A non-existent component that is
//     itself a (dangling) symlink is rejected, because writing through it would
//     create the target wherever it points.
//   - The path is allowed iff its canonical form equals, or is a descendant
//     of, some canonical root — decided with filepath.Rel and a proper ".."
//     path-segment test.
//   - No roots configured → nothing is allowed (fail closed).
//
// With the Bash tool enabled this is defense-in-depth, not a security boundary:
// a shell can read or write anything the gateway's OS user can.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"conduit/internal/config"
)

// ErrOutsideSandbox is returned when a path resolves outside every root.
var ErrOutsideSandbox = errors.New("path is outside the sandbox")

// ErrNoRoots is returned when the sandbox has no roots configured.
var ErrNoRoots = errors.New("sandbox has no allowed paths configured")

// Sandbox is an immutable set of allowed root directories.
type Sandbox struct {
	roots []string // as configured (absolute, cleaned); canonicalized per call
}

// New builds a sandbox from a workspace dir and a list of allowed paths.
// Both are treated as roots; empty strings are ignored.
func New(workspaceDir string, allowedPaths []string) *Sandbox {
	s := &Sandbox{}
	seen := map[string]bool{}
	add := func(p string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		if !seen[abs] {
			seen[abs] = true
			s.roots = append(s.roots, abs)
		}
	}
	for _, p := range allowedPaths {
		add(p)
	}
	add(workspaceDir)
	return s
}

// FromConfig builds a sandbox from the tools sandbox config.
func FromConfig(cfg config.SandboxConfig) *Sandbox {
	return New(cfg.WorkspaceDir, cfg.AllowedPaths)
}

// Roots returns the absolute (not symlink-resolved) roots.
func (s *Sandbox) Roots() []string {
	out := make([]string, len(s.roots))
	copy(out, s.roots)
	return out
}

// Resolve returns the canonical absolute path for path if it lies inside the
// sandbox. Callers should perform their I/O on the returned path so that what
// was checked is what gets opened.
func (s *Sandbox) Resolve(path string) (string, error) {
	if s == nil || len(s.roots) == 0 {
		return "", ErrNoRoots
	}
	real, err := Canonicalize(path)
	if err != nil {
		return "", err
	}
	for _, root := range s.roots {
		realRoot, err := Canonicalize(root)
		if err != nil {
			continue
		}
		if Within(realRoot, real) {
			return real, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrOutsideSandbox, path)
}

// Allowed reports whether path lies inside the sandbox.
func (s *Sandbox) Allowed(path string) bool {
	_, err := s.Resolve(path)
	return err == nil
}

// Canonicalize makes path absolute and resolves symlinks on the deepest
// existing ancestor; missing trailing components are appended lexically.
// A missing component that is actually a dangling symlink is an error.
func Canonicalize(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(path) // also Cleans, collapsing ".." lexically
	if err != nil {
		return "", err
	}

	cur := abs
	var tail []string
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		// cur does not resolve. If it exists as a link (dangling, or a loop
		// reported as ENOENT) refuse: following it on write would land
		// wherever the link points.
		if _, lerr := os.Lstat(cur); lerr == nil {
			return "", fmt.Errorf("%w: dangling symlink %s", ErrOutsideSandbox, cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the filesystem root without finding anything that exists.
			return abs, nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// Within reports whether target equals root or is a descendant of it. Both
// must be absolute and clean. Unlike strings.HasPrefix(rel, ".."), a child
// named "..notes" is correctly treated as inside.
func Within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// EscapingSymlinks lists the direct children of each root that are symlinks
// resolving outside every root, mapped to their targets. It is used at startup
// to warn operators whose workspace relies on such links (e.g. a "shared" link
// to a NAS mount): those paths are now denied until the target itself is added
// to tools.sandbox.allowed_paths. Only one level is scanned to keep it cheap.
func (s *Sandbox) EscapingSymlinks() map[string]string {
	out := map[string]string{}
	if s == nil {
		return out
	}
	for _, root := range s.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			p := filepath.Join(root, e.Name())
			if !s.Allowed(p) {
				target, _ := os.Readlink(p)
				out[p] = target
			}
		}
	}
	return out
}
