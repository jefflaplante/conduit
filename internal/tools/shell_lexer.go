package tools

// shell_lexer.go is a small, conservative shell lexer used only by the Bash
// command policy (conduit-23hg). It never executes anything: it splits a
// command line into simple commands so the denylist can match command words
// instead of raw substrings.
//
// It understands: ; && || | |& & newlines, ( ) subshells, $( ) and backtick
// command substitution (also inside double quotes), <( ) / >( ) process
// substitution, single/double/$'' quoting, backslash escapes, line
// continuations, redirections (with fd prefixes), heredocs (<< and <<-,
// quoted or not) and # comments. It is not a full POSIX parser; anything it
// cannot tokenize (unbalanced quotes, unterminated substitutions, excessive
// nesting) returns errShellSyntax and the caller falls back to the legacy
// substring check.

import (
	"errors"
	"strings"
)

var errShellSyntax = errors.New("shell command could not be tokenized")

// maxShellDepth bounds nesting of subshells/substitutions and sh -c recursion.
const maxShellDepth = 16

// shWord is one shell word after quote removal.
type shWord struct {
	text    string // word text with quotes and escapes removed
	quoted  bool   // some part of the word was quoted or escaped
	dynamic bool   // contains a $-expansion or command substitution
	// firstQuote is the byte offset in text where the first quoted/escaped
	// byte landed, or -1. Used to decide whether NAME=value is an assignment.
	firstQuote int
}

// shCmd is one simple command: its words, redirection targets, and whether
// it receives a pipe (it follows | or |&).
type shCmd struct {
	words     []shWord
	redirects []string
	afterPipe bool
}

// shParse is the lexer output. Nested substitutions and subshells are
// flattened into cmds. legacyTexts holds regions whose contents the shell
// would still expand but that are not tokenized (unquoted heredoc bodies
// containing substitutions, complex ${...} expansions); the policy runs the
// legacy substring check over them.
type shParse struct {
	cmds        []shCmd
	legacyTexts []string
}

type heredocSpec struct {
	delim  string
	quoted bool
	strip  bool // <<- strips leading tabs from body lines and the delimiter
}

const (
	redirNone = iota
	redirFile
	redirHeredoc
	redirHeredocStrip
)

type wordBuilder struct {
	buf        strings.Builder
	started    bool
	quoted     bool
	dynamic    bool
	firstQuote int
}

func (w *wordBuilder) addByte(c byte, quoted bool) {
	if !w.started {
		w.started = true
		w.firstQuote = -1
	}
	if quoted {
		w.markQuoted()
	}
	w.buf.WriteByte(c)
}

func (w *wordBuilder) addString(s string, quoted bool) {
	if !w.started {
		w.started = true
		w.firstQuote = -1
	}
	if quoted {
		w.markQuoted()
	}
	w.buf.WriteString(s)
}

func (w *wordBuilder) markQuoted() {
	if !w.started {
		w.started = true
		w.firstQuote = -1
	}
	if !w.quoted {
		w.quoted = true
		w.firstQuote = w.buf.Len()
	}
}

func (w *wordBuilder) markDynamic() {
	if !w.started {
		w.started = true
		w.firstQuote = -1
	}
	w.dynamic = true
}

func (w *wordBuilder) isFD() bool {
	if !w.started || w.quoted || w.dynamic || w.buf.Len() == 0 {
		return false
	}
	for _, c := range []byte(w.buf.String()) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (w *wordBuilder) reset() { *w = wordBuilder{} }

type shLexer struct {
	s        string
	i        int
	depth    int
	out      *shParse
	heredocs []heredocSpec
}

// parseShell tokenizes a full command line.
func parseShell(s string) (*shParse, error) {
	l := &shLexer{s: s, out: &shParse{}}
	if err := l.parseList(0); err != nil {
		return nil, err
	}
	return l.out, nil
}

func (l *shLexer) peek(off int) byte {
	if l.i+off < len(l.s) {
		return l.s[l.i+off]
	}
	return 0
}

// parseList lexes simple commands until term (')' or '`') or, for term 0,
// end of input.
func (l *shLexer) parseList(term byte) error {
	l.depth++
	defer func() { l.depth-- }()
	if l.depth > maxShellDepth {
		return errShellSyntax
	}

	var (
		cur   shCmd
		w     wordBuilder
		redir = redirNone
	)
	endWord := func() {
		if !w.started {
			return
		}
		word := shWord{text: w.buf.String(), quoted: w.quoted, dynamic: w.dynamic, firstQuote: w.firstQuote}
		switch redir {
		case redirHeredoc, redirHeredocStrip:
			l.heredocs = append(l.heredocs, heredocSpec{delim: word.text, quoted: word.quoted, strip: redir == redirHeredocStrip})
		case redirFile:
			cur.redirects = append(cur.redirects, word.text)
		default:
			cur.words = append(cur.words, word)
		}
		redir = redirNone
		w.reset()
	}
	endCmd := func(nextAfterPipe bool) {
		endWord()
		redir = redirNone
		if len(cur.words) > 0 || len(cur.redirects) > 0 {
			l.out.cmds = append(l.out.cmds, cur)
		}
		cur = shCmd{afterPipe: nextAfterPipe}
	}

	for l.i < len(l.s) {
		c := l.s[l.i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			endWord()
			l.i++
		case c == '\\':
			switch {
			case l.peek(1) == '\n':
				l.i += 2 // line continuation
			case l.i+1 < len(l.s):
				w.addByte(l.s[l.i+1], true)
				l.i += 2
			default:
				l.i++
			}
		case c == '\n':
			endCmd(false)
			l.i++
			l.readHeredocBodies()
		case c == ';':
			endCmd(false)
			l.i++
		case c == '&':
			switch l.peek(1) {
			case '&':
				endCmd(false)
				l.i += 2
			case '>':
				endWord()
				l.i += 2
				if l.peek(0) == '>' {
					l.i++
				}
				redir = redirFile
			default:
				endCmd(false)
				l.i++
			}
		case c == '|':
			if l.peek(1) == '|' {
				endCmd(false)
				l.i += 2
			} else {
				endCmd(true)
				l.i++
				if l.peek(0) == '&' {
					l.i++
				}
			}
		case c == '(':
			endCmd(false)
			l.i++
			if err := l.parseList(')'); err != nil {
				return err
			}
		case c == ')':
			endCmd(false)
			l.i++
			if term == ')' {
				return nil
			}
			// Stray ')' (e.g. a case pattern): treat as a separator.
		case c == '`':
			if term == '`' {
				endCmd(false)
				l.i++
				return nil
			}
			w.markDynamic()
			l.i++
			if err := l.parseList('`'); err != nil {
				return err
			}
		case c == '\'':
			end := strings.IndexByte(l.s[l.i+1:], '\'')
			if end < 0 {
				return errShellSyntax
			}
			w.addString(l.s[l.i+1:l.i+1+end], true) // '' still yields an (empty) word
			l.i += end + 2
		case c == '"':
			if err := l.readDouble(&w); err != nil {
				return err
			}
		case c == '$':
			if err := l.readDollar(&w); err != nil {
				return err
			}
		case c == '<' || c == '>':
			if w.isFD() {
				w.reset() // "2>" style fd prefix
			} else {
				endWord()
			}
			if l.peek(1) == '(' { // process substitution <( ) / >( )
				l.i += 2
				w.markDynamic()
				w.addString("<(...)", false)
				if err := l.parseList(')'); err != nil {
					return err
				}
				continue
			}
			op := l.s[l.i:min(l.i+3, len(l.s))]
			switch {
			case strings.HasPrefix(op, "<<<"):
				l.i += 3
				redir = redirFile
			case strings.HasPrefix(op, "<<-"):
				l.i += 3
				redir = redirHeredocStrip
			case strings.HasPrefix(op, "<<"):
				l.i += 2
				redir = redirHeredoc
			case strings.HasPrefix(op, "<>"), strings.HasPrefix(op, "<&"),
				strings.HasPrefix(op, ">>"), strings.HasPrefix(op, ">&"), strings.HasPrefix(op, ">|"):
				l.i += 2
				redir = redirFile
			default:
				l.i++
				redir = redirFile
			}
		case c == '#' && !w.started:
			if nl := strings.IndexByte(l.s[l.i:], '\n'); nl >= 0 {
				l.i += nl
			} else {
				l.i = len(l.s)
			}
		default:
			w.addByte(c, false)
			l.i++
		}
	}
	if term != 0 {
		return errShellSyntax // unterminated $( ), ( ) or backtick
	}
	endCmd(false)
	return nil
}

// readDouble consumes a double-quoted string starting at l.s[l.i] == '"'.
// $( ), backticks and $var still expand inside double quotes.
func (l *shLexer) readDouble(w *wordBuilder) error {
	l.i++
	w.markQuoted()
	for l.i < len(l.s) {
		c := l.s[l.i]
		switch c {
		case '"':
			l.i++
			return nil
		case '\\':
			switch n := l.peek(1); n {
			case '$', '`', '"', '\\':
				w.addByte(n, true)
				l.i += 2
			case '\n':
				l.i += 2
			default:
				w.addByte('\\', true)
				l.i++
			}
		case '$':
			if err := l.readDollar(w); err != nil {
				return err
			}
		case '`':
			w.markDynamic()
			l.i++
			if err := l.parseList('`'); err != nil {
				return err
			}
		default:
			w.addByte(c, true)
			l.i++
		}
	}
	return errShellSyntax
}

// readDollar consumes a $-construct starting at l.s[l.i] == '$'.
func (l *shLexer) readDollar(w *wordBuilder) error {
	n := l.peek(1)
	switch {
	case n == '(':
		w.markDynamic()
		w.addString("$(...)", false)
		l.i += 2
		return l.parseList(')')
	case n == '{':
		end := strings.IndexByte(l.s[l.i+2:], '}')
		if end < 0 {
			return errShellSyntax
		}
		body := l.s[l.i : l.i+2+end+1]
		if strings.Contains(body, "$(") || strings.Contains(body, "`") {
			l.out.legacyTexts = append(l.out.legacyTexts, body)
		}
		w.markDynamic()
		w.addString(body, false)
		l.i += 2 + end + 1
	case n == '\'': // $'...' ANSI-C quoting; escapes can hide words
		j := l.i + 2
		for j < len(l.s) && l.s[j] != '\'' {
			if l.s[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(l.s) {
			return errShellSyntax
		}
		w.markDynamic()
		w.addString(l.s[l.i+2:j], true)
		l.i = j + 1
	case n == '"': // $"..." locale string: same as "..."
		l.i++
	case isShellNameByte(n) || strings.IndexByte("@*#?$!-", n) >= 0:
		j := l.i + 1
		if isShellNameByte(n) && !(n >= '0' && n <= '9') {
			for j < len(l.s) && isShellNameByte(l.s[j]) {
				j++
			}
		} else {
			j++
		}
		w.markDynamic()
		w.addString(l.s[l.i:j], false)
		l.i = j
	default:
		w.addByte('$', false)
		l.i++
	}
	return nil
}

// readHeredocBodies consumes the bodies of heredocs opened on the line just
// ended. Bodies are data, not commands; an unquoted body that contains a
// command substitution is handed to the legacy substring check.
func (l *shLexer) readHeredocBodies() {
	for _, h := range l.heredocs {
		var body strings.Builder
		for l.i < len(l.s) {
			line := l.s[l.i:]
			if nl := strings.IndexByte(line, '\n'); nl >= 0 {
				line = line[:nl]
				l.i += nl + 1
			} else {
				l.i = len(l.s)
			}
			cmp := line
			if h.strip {
				cmp = strings.TrimLeft(line, "\t")
			}
			if cmp == h.delim {
				break
			}
			body.WriteString(line)
			body.WriteByte('\n')
		}
		if !h.quoted {
			if b := body.String(); strings.Contains(b, "$(") || strings.Contains(b, "`") {
				l.out.legacyTexts = append(l.out.legacyTexts, b)
			}
		}
	}
	l.heredocs = nil
}

func isShellNameByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// isAssignment reports whether w is a NAME=value prefix assignment.
func (w shWord) isAssignment() bool {
	eq := strings.IndexByte(w.text, '=')
	if eq <= 0 || (w.firstQuote >= 0 && w.firstQuote < eq) {
		return false
	}
	name := strings.TrimSuffix(w.text[:eq], "+")
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isShellNameByte(name[i]) {
			return false
		}
	}
	return true
}
