package tools

// bash_policy.go implements the Bash command denylist matchers
// (conduit-23hg). It inspects command strings only and never runs anything.
//
// legacy (default): each denylist entry is a case-insensitive substring
// match over the whole command, with runs of spaces collapsed.
//
// command_position: the command is tokenized (shell_lexer.go) and entries
// match only what the shell would actually execute:
//   - "name args..." entries match a simple command whose command word
//     (basename, quotes stripped, after VAR=value prefixes, reserved words
//     and wrappers such as sudo/env/timeout/xargs) is name and whose
//     arguments contain every listed flag (short flags compared as a set, so
//     -rf == -r -f == -fr; --recursive/--force count as -r/-f) and every
//     listed positional argument (paths cleaned, so // == /).
//   - "|name" / "| name" entries match name in command position after a pipe.
//   - ">target" entries match a redirection to target (/dev/ targets also
//     match by prefix, so "> /dev/sda" covers /dev/sda1).
//   - entries that are none of the above (e.g. the fork-bomb string) keep
//     literal substring matching.
//   - sh/bash/... -c strings, eval arguments and env -S strings are parsed
//     recursively; $( ), backticks and subshells are commands too.
//   - quoted text, heredoc bodies and ordinary arguments are never scanned.
//   - fail closed: if the command (or a nested -c string) cannot be
//     tokenized, if a command word is itself an expansion ($CMD, $(...)), or
//     an unquoted heredoc body contains a substitution, the legacy substring
//     check runs over that text.
//
// strict_autonomous (default true, command_position only): sessions with no
// human in the loop also get the legacy substring check.

import (
	"context"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// denyMatch describes why a command was denied. Pattern is empty when the
// command is allowed.
type denyMatch struct {
	Pattern string // the denylist entry as configured
	Mode    string // effective denylist mode
	Reason  string // where/how it matched, for the model and the audit log
}

// legacyDenylistMatch is the original substring matcher: lowercase, collapse
// runs of spaces, strings.Contains per pattern.
func legacyDenylistMatch(command string, patterns []string) string {
	normalized := strings.ToLower(strings.TrimSpace(command))
	for strings.Contains(normalized, "  ") {
		normalized = strings.ReplaceAll(normalized, "  ", " ")
	}
	for _, pattern := range patterns {
		if strings.Contains(normalized, strings.ToLower(pattern)) {
			return pattern
		}
	}
	return ""
}

var warnDenylistModeOnce sync.Once

// checkCommandPolicy applies the configured denylist mode to command.
func (t *ExecTool) checkCommandPolicy(ctx context.Context, command string) denyMatch {
	cfg := t.registry.sandboxCfg
	patterns := t.getEffectiveDenylist()
	mode, ok := cfg.EffectiveDenylistMode()
	if !ok {
		warnDenylistModeOnce.Do(func() {
			log.Printf("[Exec] WARNING: unknown tools.sandbox.denylist_mode %q; using %q", cfg.DenylistMode, mode)
		})
	}
	if mode != config.DenylistModeCommandPosition {
		if p := legacyDenylistMatch(command, patterns); p != "" {
			return denyMatch{Pattern: p, Mode: mode, Reason: "substring match anywhere in the command"}
		}
		return denyMatch{}
	}

	if m := commandPositionMatch(command, patterns); m.Pattern != "" {
		m.Mode = mode
		return m
	}
	if cfg.StrictAutonomousEnabled() && isAutonomousSession(ctx) {
		if p := legacyDenylistMatch(command, patterns); p != "" {
			return denyMatch{Pattern: p, Mode: mode,
				Reason: "substring match (strict_autonomous: no human in the loop for this session)"}
		}
	}
	return denyMatch{}
}

// isAutonomousSession reports whether the current turn has no live human:
// heartbeat/cron/sub-agent sessions, wakes, or an explicit non-interactive
// origin. An unknown origin (tests, MCP) is treated as interactive; this
// only selects the stricter matcher, it never grants anything.
func isAutonomousSession(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if types.WakeSource(ctx) != "" {
		return true
	}
	key := types.RequestSessionKey(ctx)
	for _, prefix := range []string{"heartbeat_", "cron_", "subagent_"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	if o, ok := approval.OriginFrom(ctx); ok && !o.Interactive {
		return true
	}
	return false
}

// --- rules -----------------------------------------------------------------

const (
	ruleCommand = iota
	rulePipe
	ruleRedirect
	ruleLiteral
)

type denyRule struct {
	pattern     string
	kind        int
	name        string // command / interpreter name, or redirect target
	prefix      bool   // "mkfs." style: name is a prefix of the command word
	short       map[byte]bool
	long        []string
	positionals []string
	literal     string // lowercased pattern for ruleLiteral
}

// longFlagAliases maps common long options onto their short forms so
// "rm --recursive --force /" matches "rm -rf /".
var longFlagAliases = map[string]byte{"--recursive": 'r', "--force": 'f'}

func compileDenyRule(pattern string) (denyRule, bool) {
	p := strings.ToLower(strings.TrimSpace(pattern))
	for strings.Contains(p, "  ") {
		p = strings.ReplaceAll(p, "  ", " ")
	}
	if p == "" {
		return denyRule{}, false
	}
	literal := denyRule{pattern: pattern, kind: ruleLiteral, literal: p}

	switch p[0] {
	case '|':
		name := strings.TrimSpace(p[1:])
		if isPlainCommandName(name) {
			return denyRule{pattern: pattern, kind: rulePipe, name: name}, true
		}
		return literal, true
	case '>':
		target := strings.TrimSpace(strings.TrimLeft(p, ">"))
		if target != "" && !strings.ContainsAny(target, " \t") {
			return denyRule{pattern: pattern, kind: ruleRedirect, name: normalizeShellArg(target)}, true
		}
		return literal, true
	}

	parsed, err := parseShell(p)
	if err != nil || len(parsed.cmds) != 1 || len(parsed.legacyTexts) > 0 ||
		len(parsed.cmds[0].redirects) > 0 || parsed.cmds[0].afterPipe {
		return literal, true
	}
	words := parsed.cmds[0].words
	if len(words) == 0 || words[0].dynamic || !isPlainCommandName(words[0].text) {
		return literal, true
	}
	args := make([]string, 0, len(words)-1)
	for _, w := range words[1:] {
		args = append(args, w.text)
	}
	short, long, pos := argFacts(args)
	r := denyRule{pattern: pattern, kind: ruleCommand, name: words[0].text, short: short}
	r.prefix = strings.HasSuffix(r.name, ".")
	for f := range long {
		r.long = append(r.long, f)
	}
	for a := range pos {
		r.positionals = append(r.positionals, a)
	}
	return r, true
}

func isPlainCommandName(s string) bool {
	if s == "" || !(isShellNameByte(s[0])) {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isShellNameByte(c) && c != '.' && c != '-' && c != '+' {
			return false
		}
	}
	return true
}

// normalizeShellArg lowercases an argument and cleans path-like values so
// "//", "/./" and "/" compare equal.
func normalizeShellArg(a string) string {
	a = strings.ToLower(a)
	if len(a) > 1 && (a[0] == '/' || a[0] == '~' || a[0] == '$') {
		a = path.Clean(a)
	}
	return a
}

// argFacts splits arguments into a short-flag set, long flags and
// positionals (normalized). Everything after "--" is positional.
func argFacts(args []string) (short map[byte]bool, long map[string]bool, pos map[string]bool) {
	short, long, pos = map[byte]bool{}, map[string]bool{}, map[string]bool{}
	endOpts := false
	for _, a := range args {
		a = strings.ToLower(a)
		switch {
		case !endOpts && a == "--":
			endOpts = true
		case !endOpts && strings.HasPrefix(a, "--"):
			if eq := strings.IndexByte(a, '='); eq > 0 {
				a = a[:eq]
			}
			if c, ok := longFlagAliases[a]; ok {
				short[c] = true
			} else {
				long[a] = true
			}
		case !endOpts && len(a) > 1 && a[0] == '-':
			for i := 1; i < len(a); i++ {
				short[a[i]] = true
			}
		default:
			pos[normalizeShellArg(a)] = true
		}
	}
	return short, long, pos
}

// --- command-position analysis ---------------------------------------------

// shInvocation is one command word the shell would execute, with its args.
type shInvocation struct {
	name      string // lowercased basename of the command word
	args      []string
	afterPipe bool
}

type shAnalysis struct {
	invocations []shInvocation
	redirects   []string
	legacyTexts []string // texts that get the legacy substring check
}

// shellReserved are words that may precede a command word.
var shellReserved = map[string]bool{
	"!": true, "{": true, "}": true, "if": true, "then": true, "else": true, "elif": true,
	"fi": true, "do": true, "done": true, "while": true, "until": true, "esac": true,
}

// shellInterpreters get their -c string parsed recursively; a pipe into one
// of them matches "|sh"-style entries.
var shellInterpreters = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true, "ash": true, "mksh": true, "fish": true,
}

// commandWrapper describes a command that runs another command: options that
// take a separate value, and how many positionals precede the wrapped command.
type commandWrapper struct {
	argOpts     map[string]bool
	positionals int
}

func optSet(opts ...string) map[string]bool {
	m := make(map[string]bool, len(opts))
	for _, o := range opts {
		m[o] = true
	}
	return m
}

// Option names are case-sensitive (xargs -p takes no value, -P does).
var commandWrappers = map[string]commandWrapper{
	"sudo": {argOpts: optSet("-u", "-g", "-h", "-p", "-C", "-D", "-R", "-T", "-U", "-r", "-t",
		"--user", "--group", "--host", "--prompt", "--close-from", "--chdir", "--role", "--type",
		"--other-user", "--command-timeout", "--chroot")},
	"doas":    {argOpts: optSet("-u", "-C")},
	"env":     {argOpts: optSet("-u", "-C", "-S", "--unset", "--chdir", "--split-string")},
	"nice":    {argOpts: optSet("-n", "--adjustment")},
	"ionice":  {argOpts: optSet("-c", "-n", "-p", "--class", "--classdata")},
	"nohup":   {},
	"timeout": {argOpts: optSet("-s", "-k", "--signal", "--kill-after"), positionals: 1},
	"command": {},
	"builtin": {},
	"exec":    {argOpts: optSet("-a")},
	"xargs": {argOpts: optSet("-a", "-d", "-E", "-I", "-L", "-n", "-P", "-s",
		"--arg-file", "--delimiter", "--eof", "--replace", "--max-lines", "--max-args", "--max-procs", "--max-chars")},
	"stdbuf":  {argOpts: optSet("-i", "-o", "-e")},
	"time":    {argOpts: optSet("-f", "-o", "--format", "--output")},
	"busybox": {},
	"setsid":  {},
	"chroot":  {argOpts: optSet("--userspec", "--groups"), positionals: 1},
}

func analyzeShell(cmdline string, depth int, a *shAnalysis) {
	if depth > maxShellDepth {
		a.legacyTexts = append(a.legacyTexts, cmdline)
		return
	}
	p, err := parseShell(cmdline)
	if err != nil {
		a.legacyTexts = append(a.legacyTexts, cmdline) // fail closed
		return
	}
	a.legacyTexts = append(a.legacyTexts, p.legacyTexts...)
	for _, c := range p.cmds {
		for _, r := range c.redirects {
			a.redirects = append(a.redirects, normalizeShellArg(r))
		}
		words := c.words
		i := 0
		for i < len(words) && (words[i].isAssignment() || (!words[i].quoted && shellReserved[words[i].text])) {
			i++
		}
		for i < len(words) {
			w := words[i]
			if w.dynamic {
				// The command word is an expansion; we cannot know what runs.
				a.legacyTexts = append(a.legacyTexts, cmdline)
				break
			}
			name := path.Base(strings.ToLower(w.text))
			rest := words[i+1:]
			args := make([]string, len(rest))
			for k, r := range rest {
				args[k] = r.text
			}
			a.invocations = append(a.invocations, shInvocation{name: name, args: args, afterPipe: c.afterPipe})

			switch {
			case shellInterpreters[name]:
				if s, ok := shellCommandString(args); ok {
					analyzeShell(s, depth+1, a)
				}
			case name == "eval":
				analyzeShell(strings.Join(args, " "), depth+1, a)
			case name == "env":
				if s, ok := envSplitString(args); ok {
					analyzeShell(s, depth+1, a)
				}
			}

			next, ok := skipWrapper(name, rest)
			if !ok {
				break
			}
			i += 1 + next
		}
	}
}

// skipWrapper returns how many words of rest belong to wrapper name
// (options, env assignments, fixed positionals) before the wrapped command.
func skipWrapper(name string, rest []shWord) (int, bool) {
	spec, ok := commandWrappers[name]
	if !ok {
		return 0, false
	}
	j := 0
	for j < len(rest) {
		w := rest[j]
		t := w.text
		if t == "--" {
			j++
			break
		}
		if name == "env" && w.isAssignment() {
			j++
			continue
		}
		if len(t) > 1 && t[0] == '-' {
			if spec.argOpts[t] {
				j += 2
			} else {
				j++
			}
			continue
		}
		break
	}
	for k := 0; k < spec.positionals && j < len(rest); k++ {
		j++
	}
	if j > len(rest) {
		j = len(rest)
	}
	return j, true
}

// shellCommandString finds the command string of "sh -c STRING" (also
// combined forms such as -ec / -lc).
func shellCommandString(args []string) (string, bool) {
	for j := 0; j < len(args); j++ {
		t := args[j]
		if t == "--" || len(t) < 2 || (t[0] != '-' && t[0] != '+') {
			return "", false // script path or end of options: no -c
		}
		if t[0] == '-' && t[1] != '-' && strings.IndexByte(t[1:], 'c') >= 0 {
			for k := j + 1; k < len(args); k++ {
				if !strings.HasPrefix(args[k], "-") || args[k] == "-" {
					return args[k], true
				}
			}
			return "", false
		}
		if t == "-o" || t == "+o" || t == "-O" || t == "+O" {
			j++ // option name follows
		}
	}
	return "", false
}

// envSplitString returns the argument of env -S / --split-string.
func envSplitString(args []string) (string, bool) {
	for j, t := range args {
		switch {
		case t == "-S" || t == "--split-string":
			if j+1 < len(args) {
				return args[j+1], true
			}
		case strings.HasPrefix(t, "--split-string="):
			return strings.TrimPrefix(t, "--split-string="), true
		case len(t) > 2 && strings.HasPrefix(t, "-S"):
			return t[2:], true
		case !strings.HasPrefix(t, "-"):
			return "", false
		}
	}
	return "", false
}

// commandPositionMatch evaluates patterns against the tokenized command.
func commandPositionMatch(command string, patterns []string) denyMatch {
	var a shAnalysis
	analyzeShell(command, 0, &a)

	rules := make([]denyRule, 0, len(patterns))
	for _, p := range patterns {
		if r, ok := compileDenyRule(p); ok {
			rules = append(rules, r)
		}
	}

	for _, r := range rules {
		switch r.kind {
		case ruleCommand:
			for _, inv := range a.invocations {
				if r.matchesInvocation(inv) {
					return denyMatch{Pattern: r.pattern, Reason: fmt.Sprintf("command word %q in command position", inv.name)}
				}
			}
		case rulePipe:
			for _, inv := range a.invocations {
				if inv.afterPipe && inv.name == r.name {
					return denyMatch{Pattern: r.pattern, Reason: fmt.Sprintf("pipe into %q", inv.name)}
				}
			}
		case ruleRedirect:
			for _, t := range a.redirects {
				if t == r.name || (strings.HasPrefix(r.name, "/dev/") && strings.HasPrefix(t, r.name)) {
					return denyMatch{Pattern: r.pattern, Reason: fmt.Sprintf("redirection to %q", t)}
				}
			}
		}
	}
	// Entries with no command shape keep literal matching over the command.
	var literal []string
	for _, r := range rules {
		if r.kind == ruleLiteral {
			literal = append(literal, r.pattern)
		}
	}
	if p := legacyDenylistMatch(command, literal); p != "" {
		return denyMatch{Pattern: p, Reason: "literal match (entry has no command form)"}
	}
	// Fail closed on text we could not tokenize.
	for _, text := range a.legacyTexts {
		if p := legacyDenylistMatch(text, patterns); p != "" {
			return denyMatch{Pattern: p, Reason: "substring match (fallback: text could not be tokenized or command word is an expansion)"}
		}
	}
	return denyMatch{}
}

func (r denyRule) matchesInvocation(inv shInvocation) bool {
	if r.prefix {
		if !strings.HasPrefix(inv.name, r.name) {
			return false
		}
	} else if inv.name != r.name {
		return false
	}
	if len(r.short) == 0 && len(r.long) == 0 && len(r.positionals) == 0 {
		return true
	}
	short, long, pos := argFacts(inv.args)
	for c := range r.short {
		if !short[c] {
			return false
		}
	}
	for _, f := range r.long {
		if !long[f] {
			return false
		}
	}
	for _, p := range r.positionals {
		if !pos[p] {
			return false
		}
	}
	return true
}
