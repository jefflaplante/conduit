//go:build with_ssh

package ssh

import (
	"fmt"
	"path"
	"strings"

	"conduit/internal/tools"
)

// security_shell.go holds the per-segment half of command classification
// (C3): ClassifyCommand splits a line with tools.ParseShellSegments and the
// helpers here classify each simple command, raise the tier for writes, and
// build the normalised text that blocked_patterns are matched against.

// systemWriteDirs are directories where a write is dangerous rather than a
// plain modification (the same list mv/cp use in checkDangerousArgs).
var systemWriteDirs = []string{"/bin", "/sbin", "/usr/bin", "/usr/sbin", "/etc", "/boot", "/root"}

// classifySegments classifies every simple command and returns the worst
// tier, the base command that produced it, and a reason.
func (e *SecurityEngine) classifySegments(p *tools.ShellParse) (SecurityTier, string, string) {
	if len(p.Segments) == 0 {
		return TierBlocked, "", "empty command"
	}
	var (
		worstTier    SecurityTier
		worstCommand string
		worstReason  string
		reasons      []string
	)
	for i, seg := range p.Segments {
		tier, baseCmd, reason := e.classifySegment(seg)
		if i == 0 || tierSeverity(tier) > tierSeverity(worstTier) {
			worstTier, worstCommand, worstReason = tier, baseCmd, reason
		}
		reasons = append(reasons, fmt.Sprintf("%s: %s", baseCmd, reason))
	}
	if p.ProcessSubstitution && tierSeverity(worstTier) < tierSeverity(TierModify) {
		worstTier, worstReason = TierModify, "process substitution is at least modify"
		reasons = append(reasons, worstReason)
	}
	if len(p.Untokenized) > 0 {
		// Substitutions the lexer could not split run unknown code.
		if def := SecurityTier(e.config.DefaultTier); tierSeverity(def) > tierSeverity(worstTier) {
			worstTier, worstReason = def, fmt.Sprintf("substitution that could not be analysed, using default tier: %s", def)
			reasons = append(reasons, worstReason)
		}
	}
	if len(reasons) == 1 {
		return worstTier, worstCommand, worstReason
	}
	return worstTier, worstCommand, fmt.Sprintf("command chain classification (worst): %s", strings.Join(reasons, " | "))
}

// classifySegment classifies one simple command, including what its
// redirections and heredocs do.
func (e *SecurityEngine) classifySegment(seg tools.ShellSegment) (SecurityTier, string, string) {
	tier, baseCmd, reason := TierRead, "", "redirection only"
	if len(seg.Words) > 0 {
		tier, baseCmd, reason = e.classifyWords(seg.Words)
	}
	raise := func(to SecurityTier, why string) {
		if tierSeverity(to) > tierSeverity(tier) {
			tier, reason = to, why
		}
	}
	for i, target := range seg.Redirects {
		if i < len(seg.RedirectOut) && seg.RedirectOut[i] {
			if t := writeTier(target); t != TierRead {
				raise(t, fmt.Sprintf("output redirection to %s is %s", target, t))
			}
		}
	}
	if seg.Heredoc {
		raise(TierModify, "heredoc input is at least modify")
	}
	return tier, baseCmd, reason
}

// writeTier is the tier of writing to target: read for /dev/null, the
// standard streams and fd duplication (2>&1, >&-), dangerous under a system
// directory, modify otherwise.
func writeTier(target string) SecurityTier {
	if target == "-" || isNumeric(target) && !strings.ContainsAny(target, ".-") {
		return TierRead
	}
	clean := path.Clean(target)
	switch clean {
	case "/dev/null", "/dev/stdout", "/dev/stderr":
		return TierRead
	}
	for _, dir := range systemWriteDirs {
		if clean == dir || strings.HasPrefix(clean, dir+"/") {
			return TierDangerous
		}
	}
	return TierModify
}

// checkReadArgs returns a tier above read when a read-tier command's
// arguments delete, execute or write files (find -exec, curl -o, wget).
func checkReadArgs(baseCmd string, args []string) (SecurityTier, string) {
	switch baseCmd {
	case "find":
		tier, reason := TierRead, ""
		for _, a := range args {
			switch a {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir":
				return TierDangerous, fmt.Sprintf("find %s deletes files or runs commands", a)
			case "-fprint", "-fprint0", "-fprintf", "-fls":
				tier, reason = TierModify, fmt.Sprintf("find %s writes a file", a)
			}
		}
		return tier, reason
	case "curl":
		return curlWriteTier(args)
	case "wget":
		return wgetWriteTier(args)
	}
	return TierRead, ""
}

// tierRaiser keeps the worst tier seen and its reason.
type tierRaiser struct {
	tier   SecurityTier
	reason string
}

func (r *tierRaiser) raise(to SecurityTier, why string) {
	if tierSeverity(to) > tierSeverity(r.tier) {
		r.tier, r.reason = to, why
	}
}

func (r *tierRaiser) file(cmd, target string) {
	if t := writeTier(target); t != TierRead {
		r.raise(t, fmt.Sprintf("%s writing to %s is %s", cmd, target, t))
	}
}

// optionValue returns the value of the option at args[*i]: the text after
// '=' (long) or after the flag letter (short), else the next word.
func optionValue(args []string, i *int, inline string, hasInline bool) string {
	if hasInline {
		return inline
	}
	if *i+1 < len(args) {
		*i++
		return args[*i]
	}
	return ""
}

// curlValueFlags are curl's short options that take a value.
const curlValueFlags = "AbcCdDeEFHKmoPQrtTuUwxXyYz"

func curlWriteTier(args []string) (SecurityTier, string) {
	r := &tierRaiser{tier: TierRead}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return r.tier, r.reason
		case strings.HasPrefix(a, "--"):
			name, val, has := strings.Cut(a[2:], "=")
			switch name {
			case "output", "dump-header", "cookie-jar", "trace", "trace-ascii", "stderr", "libcurl", "etag-save", "output-dir":
				r.file("curl --"+name, optionValue(args, &i, val, has))
			case "remote-name", "remote-name-all", "remote-header-name", "create-dirs":
				r.raise(TierModify, "curl --"+name+" saves a file")
			case "upload-file", "config":
				r.raise(TierModify, "curl --"+name+" is at least modify")
			}
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				c := a[j]
				if strings.IndexByte(curlValueFlags, c) < 0 {
					if c == 'O' || c == 'J' {
						r.raise(TierModify, "curl -O saves a file")
					}
					continue
				}
				val := optionValue(args, &i, a[j+1:], j+1 < len(a))
				switch c {
				case 'o', 'D', 'c':
					r.file("curl -"+string(c), val)
				case 'T', 'K':
					r.raise(TierModify, "curl -"+string(c)+" is at least modify")
				}
				break
			}
		}
	}
	return r.tier, r.reason
}

// wgetValueFlags are wget's short options that take a value.
const wgetValueFlags = "aABDeiIlOoPQRtTUwX"

func wgetWriteTier(args []string) (SecurityTier, string) {
	r := &tierRaiser{tier: TierRead}
	toStdout, spider := false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, val, has := strings.Cut(a[2:], "=")
			switch name {
			case "output-document":
				if v := optionValue(args, &i, val, has); v == "-" {
					toStdout = true
				} else {
					r.file("wget --"+name, v)
				}
			case "output-file", "append-output", "directory-prefix":
				r.file("wget --"+name, optionValue(args, &i, val, has))
			case "execute", "input-file", "config":
				r.raise(TierModify, "wget --"+name+" is at least modify")
			case "spider":
				spider = true
			}
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				c := a[j]
				if strings.IndexByte(wgetValueFlags, c) < 0 {
					continue
				}
				val := optionValue(args, &i, a[j+1:], j+1 < len(a))
				switch c {
				case 'O':
					if val == "-" {
						toStdout = true
					} else {
						r.file("wget -O", val)
					}
				case 'o', 'a', 'P':
					r.file("wget -"+string(c), val)
				case 'e', 'i':
					r.raise(TierModify, "wget -"+string(c)+" is at least modify")
				}
				break
			}
		}
	}
	if !toStdout && !spider {
		r.raise(TierModify, "wget saves the download to a file")
	}
	return r.tier, r.reason
}

// renderSegment rebuilds a simple command as text for blocked-list and
// blocked-pattern matching: quotes and escapes removed, path-like words
// cleaned (/etc//shadow, /etc/./shadow -> /etc/shadow), redirections kept
// as "> target" / "< target".
func renderSegment(seg tools.ShellSegment) string {
	parts := make([]string, 0, len(seg.Words)+2*len(seg.Redirects))
	for _, w := range seg.Words {
		parts = append(parts, cleanPathWord(w))
	}
	for i, target := range seg.Redirects {
		op := "<"
		if i < len(seg.RedirectOut) && seg.RedirectOut[i] {
			op = ">"
		}
		parts = append(parts, op, cleanPathWord(target))
	}
	return strings.Join(parts, " ")
}

func cleanPathWord(w string) string {
	if !strings.Contains(w, "/") {
		return w
	}
	return path.Clean(w)
}

// patternSubjects lists the texts blocked_patterns are matched against: the
// raw command, and when it tokenizes, the whole rendered line and each
// rendered segment (so anchored patterns such as `/$` see a segment end).
func patternSubjects(command string, p *tools.ShellParse) []string {
	subjects := []string{command}
	if p == nil {
		return subjects
	}
	var line strings.Builder
	for i, seg := range p.Segments {
		r := renderSegment(seg)
		if i > 0 {
			if seg.AfterPipe {
				line.WriteString(" | ")
			} else {
				line.WriteString(" ; ")
			}
		}
		line.WriteString(r)
		subjects = append(subjects, r)
	}
	subjects = append(subjects, line.String())
	subjects = append(subjects, p.Untokenized...)
	return subjects
}
