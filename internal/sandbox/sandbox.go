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
	"time"

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

// Defaults for the startup escaping-symlink scan (conduit-31jg.70). They keep
// the scan to well under a second on a large workspace or a slow NAS mount.
const (
	DefaultSymlinkScanDepth   = 8
	DefaultSymlinkScanEntries = 20000
	DefaultSymlinkScanTime    = 2 * time.Second
)

// SymlinkScanOptions bounds ScanEscapingSymlinks. Zero fields take defaults.
type SymlinkScanOptions struct {
	MaxDepth   int           // directory levels below each root (root = 0)
	MaxEntries int           // total directory entries examined, all roots
	MaxTime    time.Duration // wall-clock budget
}

// skipScanDirs are not descended into: large, rarely hold operator links,
// and would eat the entry budget.
var skipScanDirs = map[string]bool{".git": true, "node_modules": true}

// EscapingSymlinks lists symlinks under each root, at any depth within the
// default bounds, that resolve outside every root, mapped to their link
// targets. It is used at startup to warn operators whose workspace relies on
// such links (e.g. a "shared" link to a NAS mount): those paths are denied
// until the target itself is added to tools.sandbox.allowed_paths.
func (s *Sandbox) EscapingSymlinks() map[string]string {
	out, _ := s.ScanEscapingSymlinks(SymlinkScanOptions{})
	return out
}

// ScanEscapingSymlinks walks each root breadth-first without following
// symlinks (a link is checked, never descended), stopping at the depth,
// entry and time bounds. truncated reports that a bound was hit, so the
// result may be incomplete. conduit-31jg.70: previously only the direct
// children of each root were examined, so workspace/projects/x -> /etc went
// unreported.
func (s *Sandbox) ScanEscapingSymlinks(opts SymlinkScanOptions) (found map[string]string, truncated bool) {
	found = map[string]string{}
	if s == nil {
		return found, false
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = DefaultSymlinkScanDepth
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = DefaultSymlinkScanEntries
	}
	if opts.MaxTime <= 0 {
		opts.MaxTime = DefaultSymlinkScanTime
	}
	deadline := time.Now().Add(opts.MaxTime)

	type dir struct {
		path  string
		depth int
	}
	visited := map[string]bool{}
	queue := make([]dir, 0, len(s.roots))
	for _, root := range s.roots {
		queue = append(queue, dir{path: root})
	}
	entries := 0

	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if visited[d.path] {
			continue // overlapping roots (e.g. workspace inside an allowed path)
		}
		visited[d.path] = true
		if time.Now().After(deadline) {
			return found, true
		}

		list, err := os.ReadDir(d.path)
		if err != nil {
			continue
		}
		for _, e := range list {
			if entries >= opts.MaxEntries {
				return found, true
			}
			entries++
			p := filepath.Join(d.path, e.Name())
			switch {
			case e.Type()&os.ModeSymlink != 0:
				if !s.Allowed(p) {
					target, _ := os.Readlink(p)
					found[p] = target
				}
			case e.IsDir():
				if skipScanDirs[e.Name()] {
					continue
				}
				if d.depth+1 > opts.MaxDepth {
					truncated = true
					continue
				}
				queue = append(queue, dir{path: p, depth: d.depth + 1})
			}
		}
	}
	return found, truncated
}
