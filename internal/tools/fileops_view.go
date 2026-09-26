package tools

// conduit-31jg.39: model-facing helpers for Read (line paging) and Glob
// (doublestar pattern matching). Modelled on Claude Code's Read/Glob
// contracts, which models are trained on.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultReadLimit is the default number of lines Read returns.
	DefaultReadLimit = 2000
	// maxReadLineBytes truncates very long lines (minified JS, JSON blobs).
	maxReadLineBytes = 2000
	// readMarkerReserve is budget held back for the continuation marker.
	readMarkerReserve = 256
	// maxWholeReadBytes: files up to this size are read into memory (and
	// scanned for brain-extract hints); larger ones are streamed.
	maxWholeReadBytes = 10 << 20

	// DefaultGlobLimit caps paths returned by a Glob pattern search.
	DefaultGlobLimit = 100
	// maxGlobVisited bounds a pattern walk so `**` from a huge root returns.
	maxGlobVisited = 200000
	// maxBracePatterns bounds brace expansion.
	maxBracePatterns = 64
)

// readPage is the result of paging a file's lines.
type readPage struct {
	Text       string
	StartLine  int
	EndLine    int // last line included (0 if none)
	TotalLines int
	Truncated  bool // output ended before EOF (limit or budget)
	ByBudget   bool // ended because of the character budget
}

// renderLinePage streams r and renders lines [offset, offset+limit) in
// `cat -n` format ("%6d\t%s"), stopping early once budget bytes would be
// exceeded. It keeps scanning (without storing) to count total lines.
func renderLinePage(r io.Reader, offset, limit, budget int) (readPage, error) {
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = DefaultReadLimit
	}
	br := bufio.NewReaderSize(r, 64<<10)
	var out strings.Builder
	page := readPage{StartLine: offset}
	lineNo := 0
	stopped := false
	for {
		line, err := readCappedLine(br, maxReadLineBytes)
		if err != nil && errors.Is(err, io.EOF) && line == nil {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return page, err
		}
		lineNo++
		if !stopped && lineNo >= offset {
			if lineNo >= offset+limit {
				stopped = true
				page.Truncated = true
			} else {
				rendered := fmt.Sprintf("%6d\t%s\n", lineNo, line)
				if out.Len()+len(rendered) > budget && page.EndLine > 0 {
					stopped = true
					page.Truncated = true
					page.ByBudget = true
				} else {
					out.WriteString(rendered)
					page.EndLine = lineNo
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	page.TotalLines = lineNo
	page.Text = out.String()
	return page, nil
}

// readCappedLine reads one line (without its newline), keeping at most
// maxBytes of it (rune-safe) and discarding the rest. Returns (nil, io.EOF)
// at end of input; a final unterminated line is returned with io.EOF.
func readCappedLine(br *bufio.Reader, maxBytes int) ([]byte, error) {
	var kept []byte
	dropped := 0
	sawAny := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			sawAny = true
		}
		body := chunk
		if n := len(body); n > 0 && body[n-1] == '\n' {
			body = body[:n-1]
		}
		if room := maxBytes - len(kept); room > 0 {
			take := body
			if len(take) > room {
				take = take[:room]
			}
			kept = append(kept, take...)
			dropped += len(body) - len(take)
		} else {
			dropped += len(body)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if !sawAny && err != nil {
			return nil, err
		}
		kept = []byte(strings.TrimSuffix(string(kept), "\r"))
		if dropped > 0 {
			// Drop a rune split by the cap (at most UTFMax-1 bytes).
			for i := 0; i < utf8.UTFMax-1 && len(kept) > 0; i++ {
				if r, size := utf8.DecodeLastRune(kept); r != utf8.RuneError || size != 1 {
					break
				}
				kept = kept[:len(kept)-1]
			}
			kept = append(kept, fmt.Sprintf("… [line truncated, %d more bytes]", dropped)...)
		}
		return kept, err
	}
}

// looksBinary reports whether the first 8KB of b contain a NUL byte.
func looksBinary(b []byte) bool {
	if len(b) > 8192 {
		b = b[:8192]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// expandBraces expands {a,b} alternatives (nesting allowed), capped at
// maxBracePatterns results.
func expandBraces(p string) []string {
	open := strings.IndexByte(p, '{')
	if open < 0 {
		return []string{p}
	}
	depth, closeIdx := 0, -1
	var commas []int
	for k := open; k < len(p) && closeIdx < 0; k++ {
		switch p[k] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				closeIdx = k
			}
		case ',':
			if depth == 1 {
				commas = append(commas, k)
			}
		}
	}
	if closeIdx < 0 {
		return []string{p}
	}
	var parts []string
	start := open + 1
	for _, c := range commas {
		parts = append(parts, p[start:c])
		start = c + 1
	}
	parts = append(parts, p[start:closeIdx])
	var out []string
	for _, part := range parts {
		for _, e := range expandBraces(p[:open] + part + p[closeIdx+1:]) {
			if len(out) >= maxBracePatterns {
				return out
			}
			out = append(out, e)
		}
	}
	return out
}

// matchGlobSegments matches slash-split path segments against pattern
// segments; "**" matches zero or more whole segments, other segments use
// path.Match syntax (*, ?, [...]).
func matchGlobSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchGlobSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// splitStaticPrefix returns the leading pattern segments that contain no
// glob metacharacters (a directory to start walking from) and the rest.
func splitStaticPrefix(pattern string) (prefix, rest string) {
	segs := strings.Split(pattern, "/")
	i := 0
	for i < len(segs)-1 && !strings.ContainsAny(segs[i], "*?[{") {
		i++
	}
	return strings.Join(segs[:i], "/"), strings.Join(segs[i:], "/")
}

type globMatch struct {
	path    string
	modTime int64
}

// globResult is the outcome of a pattern search.
type globResult struct {
	Matches  []string // absolute paths, newest first, capped at limit
	Total    int      // matches found (may exceed len(Matches))
	Stopped  bool     // walk hit maxGlobVisited
	BaseDirs []string // directories walked
}

// globFiles finds files under baseDir matching pattern ("**" aware, brace
// expansion, relative to baseDir; absolute patterns allowed). allowed is
// consulted for every walk root. .git directories are skipped.
func globFiles(ctx context.Context, baseDir, pattern string, limit int, allowed func(string) bool) (globResult, error) {
	var res globResult
	if limit <= 0 {
		limit = DefaultGlobLimit
	}
	seen := map[string]bool{}
	var matches []globMatch
	visited := 0

	for _, pat := range expandBraces(filepath.ToSlash(pattern)) {
		root := baseDir
		if path.IsAbs(pat) {
			root = "/"
			pat = strings.TrimPrefix(pat, "/")
		}
		prefix, rest := splitStaticPrefix(pat)
		if prefix != "" {
			root = filepath.Join(root, filepath.FromSlash(prefix))
		}
		if allowed != nil && !allowed(root) {
			return res, fmt.Errorf("path %q is not allowed in sandbox", root)
		}
		res.BaseDirs = append(res.BaseDirs, root)
		patSegs := strings.Split(rest, "/")

		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == root {
					return err
				}
				return nil // unreadable subtree: skip
			}
			visited++
			if visited%1024 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			if visited > maxGlobVisited {
				res.Stopped = true
				return fs.SkipAll
			}
			if d.IsDir() {
				if d.Name() == ".git" && p != root {
					return fs.SkipDir
				}
				return nil
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return nil
			}
			if !matchGlobSegments(patSegs, strings.Split(filepath.ToSlash(rel), "/")) || seen[p] {
				return nil
			}
			seen[p] = true
			var mt int64
			if info, ierr := d.Info(); ierr == nil {
				mt = info.ModTime().UnixNano()
			}
			matches = append(matches, globMatch{path: p, modTime: mt})
			return nil
		})
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && prefix != "" {
				continue // static prefix dir missing: no matches for this branch
			}
			return res, err
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].modTime != matches[j].modTime {
			return matches[i].modTime > matches[j].modTime
		}
		return matches[i].path < matches[j].path
	})
	res.Total = len(matches)
	for i := 0; i < len(matches) && i < limit; i++ {
		res.Matches = append(res.Matches, matches[i].path)
	}
	return res, nil
}
