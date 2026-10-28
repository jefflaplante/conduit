package tools

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"conduit/internal/config"
	"conduit/internal/sandbox"
)

// bash_protect.go refuses Bash commands that could write a protected file
// (sandbox.Protect: the gateway's config.json and its approved snapshot).
// Those files change only through update_config, where the owner sees the
// diff and approves it.
//
// A shell is not a sandbox, so this is a best-effort tripwire, deliberately
// conservative: a command that names a protected file, its directory, or
// its file name in a position where it could be written is refused unless
// it is a known read-only program. What it cannot see:
// paths assembled at run time (variables set by earlier commands, $(...)
// output, base64 or other encodings, files written by scripts the command
// runs, an interpreter reading its program from a file). Those are left to
// the restart check (gateway config snapshot), which logs any change that
// did not come through update_config.

// readOnlyCommands only read their file arguments (output redirections are
// still checked).
var readOnlyCommands = map[string]bool{
	"cat": true, "head": true, "tail": true, "less": true, "more": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "jq": true,
	"wc": true, "diff": true, "cmp": true, "stat": true, "ls": true, "file": true,
	"sha256sum": true, "sha1sum": true, "md5sum": true, "readlink": true, "realpath": true,
	"du": true, "basename": true, "dirname": true, "echo": true, "printf": true,
	"test": true, "[": true, "true": true, "false": true, "pwd": true,
	"cd": true, "pushd": true, "popd": true, "type": true, "which": true,
}

// protectedTarget is one protected file as the checker matches it.
type protectedTarget struct {
	file string // canonical path
	dir  string // its directory
	base string // its file name
	tail string // "<dir name>/<file name>", a distinctive substring
}

func protectedTargets() []protectedTarget {
	var out []protectedTarget
	for _, p := range sandbox.ProtectedPaths() {
		dir := filepath.Dir(p)
		out = append(out, protectedTarget{
			file: p, dir: dir, base: filepath.Base(p),
			tail: filepath.Base(dir) + "/" + filepath.Base(p),
		})
	}
	return out
}

// protectedPathDenial returns why command may write a protected file, or
// "" when it does not appear to. cwd is the directory the command runs in.
func protectedPathDenial(command, cwd string) string {
	if why := configAcceptDenial(command); why != "" {
		return why
	}
	targets := protectedTargets()
	if len(targets) == 0 {
		return ""
	}
	c := &protectCheck{targets: targets, home: os.Getenv("HOME"), dirs: []string{cwd}}
	c.analyze(command, 0)
	if c.reason != "" {
		return c.reason
	}
	// Heredoc bodies are not split into words but may be a script fed to
	// an interpreter: refuse one that spells out a protected path.
	if c.heredoc {
		for _, t := range targets {
			for _, spelled := range c.spellings(t) {
				if strings.Contains(command, spelled) {
					return "the command names " + t.file + " and is not read-only"
				}
			}
		}
	}
	return ""
}

type protectCheck struct {
	targets []protectedTarget
	home    string
	dirs    []string // directories relative paths may resolve against
	reason  string
	heredoc bool
}

func (c *protectCheck) deny(why string) {
	if c.reason == "" {
		c.reason = why
	}
}

// spellings are ways a command may write t's path literally.
func (c *protectCheck) spellings(t protectedTarget) []string {
	out := []string{t.file, t.tail}
	if c.home != "" && strings.HasPrefix(t.file, c.home+"/") {
		rel := strings.TrimPrefix(t.file, c.home+"/")
		out = append(out, "~/"+rel, "$HOME/"+rel, "${HOME}/"+rel)
	}
	return out
}

func (c *protectCheck) analyze(cmdline string, depth int) {
	if depth > maxShellDepth {
		c.deny("command nests too deeply to check")
		return
	}
	p, err := parseShell(cmdline)
	if err != nil {
		// Unparseable: refuse only if it spells a protected path.
		for _, t := range c.targets {
			for _, s := range c.spellings(t) {
				if strings.Contains(cmdline, s) {
					c.deny("the command names " + t.file + " and could not be parsed")
				}
			}
		}
		return
	}
	for _, text := range p.legacyTexts {
		c.checkText(text, false)
	}
	// First pass: directories the command changes into.
	for _, cmd := range p.cmds {
		if len(cmd.words) >= 2 && (cmd.words[0].text == "cd" || cmd.words[0].text == "pushd") {
			c.dirs = append(c.dirs, c.resolve(cmd.words[1].text, c.dirs[0]))
		}
	}
	for _, cmd := range p.cmds {
		if cmd.heredoc {
			c.heredoc = true
		}
		for i, r := range cmd.redirects {
			if !cmd.redirOut[i] {
				continue
			}
			if c.refers(r, true) {
				c.deny("the command redirects output to " + r)
			}
			if p.substitution || strings.Contains(r, "$") {
				// The target may be computed: refuse if the command
				// mentions a protected file name anywhere.
				c.checkText(cmdline, true)
			}
		}
		words := cmd.words
		i := 0
		for i < len(words) && words[i].isAssignment() {
			// NAME=value may be used as a path later; refuse if it names one.
			if v := words[i].text[strings.IndexByte(words[i].text, '=')+1:]; c.refers(v, true) {
				c.deny("the command assigns a protected path: " + words[i].text)
			}
			i++
		}
		if i >= len(words) {
			continue
		}
		name := path.Base(strings.ToLower(words[i].text))
		args := words[i+1:]
		writer := !readOnlyCommands[name] || words[i].dynamic
		texts := make([]string, len(args))
		for k, a := range args {
			texts[k] = a.text
		}
		switch {
		case shellInterpreters[name]:
			if s, ok := shellCommandString(texts); ok {
				c.analyze(s, depth+1)
			}
		case name == "eval":
			c.analyze(strings.Join(texts, " "), depth+1)
		case name == "env":
			if s, ok := envSplitString(texts); ok {
				c.analyze(s, depth+1)
			}
		}
		if !writer {
			continue
		}
		for _, a := range args {
			if a.dynamic {
				c.checkText(a.text, true)
				continue
			}
			if c.refers(a.text, false) {
				c.deny(name + " could write " + a.text)
			}
			// --output=PATH, of=PATH
			if eq := strings.IndexByte(a.text, '='); eq >= 0 && c.refers(a.text[eq+1:], false) {
				c.deny(name + " could write " + a.text)
			}
		}
	}
}

// checkText refuses text that spells a protected path or, when loose, its
// file name (an expansion whose value we cannot know).
func (c *protectCheck) checkText(text string, loose bool) {
	for _, t := range c.targets {
		for _, s := range c.spellings(t) {
			if strings.Contains(text, s) {
				c.deny("the command names " + t.file + " in text that cannot be checked")
				return
			}
		}
		if loose && strings.Contains(text, t.base) {
			c.deny("the command builds a path ending in " + t.base + " at run time")
			return
		}
	}
}

// expand applies ~ and $HOME / $PWD expansion the way a shell would for a
// leading word part.
func (c *protectCheck) expand(s string) string {
	switch {
	case s == "~" || strings.HasPrefix(s, "~/"):
		if c.home != "" {
			return c.home + s[1:]
		}
	case strings.HasPrefix(s, "$HOME/") || s == "$HOME":
		return c.home + strings.TrimPrefix(s, "$HOME")
	case strings.HasPrefix(s, "${HOME}"):
		return c.home + strings.TrimPrefix(s, "${HOME}")
	case strings.HasPrefix(s, "$PWD"):
		return c.dirs[0] + strings.TrimPrefix(s, "$PWD")
	}
	return s
}

func (c *protectCheck) resolve(s, dir string) string {
	s = c.expand(s)
	if filepath.IsAbs(s) {
		return filepath.Clean(s)
	}
	return filepath.Join(dir, s)
}

// refers reports whether s may name a protected file or, unless fileOnly,
// its directory.
func (c *protectCheck) refers(s string, fileOnly bool) bool {
	if s == "" {
		return false
	}
	for _, t := range c.targets {
		for _, sp := range c.spellings(t) {
			if strings.Contains(s, sp) {
				return true
			}
		}
		for _, dir := range c.dirs {
			p := c.resolve(s, dir)
			if strings.ContainsAny(s, "*?[") {
				if ok, _ := filepath.Match(p, t.file); ok {
					return true
				}
				if ok, _ := filepath.Match(p, t.dir); ok && !fileOnly {
					return true
				}
				continue
			}
			if sandbox.IsProtectedFile(p, t.file) {
				return true
			}
			if !fileOnly {
				if real, err := sandbox.Canonicalize(p); err == nil && real == t.dir {
					return true
				}
			}
		}
		// The bare file name, e.g. "find / -name config.json -delete" or a
		// script string: refuse unless it plainly is another existing file.
		if strings.Contains(s, t.base) && !c.otherExistingFile(s) {
			return true
		}
	}
	return false
}

// otherExistingFile reports whether s resolves, from the command's
// directory, to an existing regular file that is not protected.
func (c *protectCheck) otherExistingFile(s string) bool {
	if strings.ContainsAny(s, "*?[") {
		return false
	}
	p := c.resolve(s, c.dirs[0])
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return sandbox.CheckWrite(p) == nil
}

// configAcceptDenial refuses `conduit config accept` (approving the config
// file is the owner's step, never the agent's) and any command that names
// the variable that marks the agent's shell, since unsetting it is the
// first step around the CLI's own refusal. Quotes and backslashes are
// dropped first, so "config" 'accept' or con\fig still match.
func configAcceptDenial(command string) string {
	flat := strings.NewReplacer(`"`, "", "'", "", `\`, "").Replace(command)
	if strings.Contains(flat, config.ToolShellEnv) {
		return "the command names " + config.ToolShellEnv
	}
	if strings.Contains(" "+strings.Join(strings.Fields(strings.ToLower(flat)), " ")+" ", " "+config.AcceptCommand+" ") {
		return "`conduit " + config.AcceptCommand + "` approves the config file and only the owner may run it"
	}
	return ""
}
