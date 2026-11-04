package tools

// shell_segments.go exports the Bash policy's shell lexer (shell_lexer.go)
// to other command classifiers, such as the SSH tool's security engine
// (conduit C3), so they split command lines the same way instead of keeping
// their own splitter.

// ShellSegment is one simple command of a command line. Commands nested in
// $( ), backticks, <( ), >( ) and ( ) subshells are separate segments.
type ShellSegment struct {
	// Words are the command's words with quotes and escapes removed.
	// A command substitution word reads "$(...)", a process substitution
	// "<(...)".
	Words []string
	// Redirects are the redirection targets, in order.
	Redirects []string
	// RedirectOut[i] reports whether Redirects[i] is an output redirection
	// (>, >>, >|, >&, &>, &>>, <>).
	RedirectOut []bool
	// Heredoc reports a << / <<- heredoc or <<< here-string.
	Heredoc bool
	// AfterPipe reports that the segment reads a pipe (follows | or |&).
	AfterPipe bool
}

// ShellParse is the result of ParseShellSegments.
type ShellParse struct {
	Segments []ShellSegment
	// CommandSubstitution reports $( ) or backtick substitution anywhere.
	CommandSubstitution bool
	// ProcessSubstitution reports <( ) or >( ) anywhere.
	ProcessSubstitution bool
	// Untokenized holds text the shell would still expand but the lexer did
	// not split into commands (an unquoted heredoc body or a ${...}
	// expansion containing a substitution). Callers must treat it as
	// unknown code.
	Untokenized []string
}

// ParseShellSegments splits a command line into simple commands. It returns
// an error when the line cannot be tokenized (unbalanced quotes,
// unterminated substitutions, excessive nesting); callers must then fail
// closed.
func ParseShellSegments(s string) (*ShellParse, error) {
	p, err := parseShell(s)
	if err != nil {
		return nil, err
	}
	out := &ShellParse{
		CommandSubstitution: p.substitution,
		ProcessSubstitution: p.procSubst,
		Untokenized:         p.legacyTexts,
	}
	for _, c := range p.cmds {
		seg := ShellSegment{
			Redirects:   c.redirects,
			RedirectOut: c.redirOut,
			Heredoc:     c.heredoc,
			AfterPipe:   c.afterPipe,
		}
		for _, w := range c.words {
			seg.Words = append(seg.Words, w.text)
		}
		out.Segments = append(out.Segments, seg)
	}
	return out, nil
}
