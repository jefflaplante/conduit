package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseShellSegments_SplitsAndFlags(t *testing.T) {
	p, err := ParseShellSegments("ls -la 2>&1 | grep x > out.txt && cat < in.txt; echo \"$(id)\" & tee >(wc) <<< hi\nsort 1<>f &>> log")
	require.NoError(t, err)
	assert.True(t, p.CommandSubstitution)
	assert.True(t, p.ProcessSubstitution)

	var words [][]string
	for _, s := range p.Segments {
		words = append(words, s.Words)
	}
	assert.Equal(t, [][]string{
		{"ls", "-la"}, {"grep", "x"}, {"cat"}, {"id"}, {"echo", "$(...)"}, {"wc"}, {"tee", "<(...)"}, {"sort"},
	}, words)

	assert.Equal(t, []string{"1"}, p.Segments[0].Redirects)
	assert.Equal(t, []bool{true}, p.Segments[0].RedirectOut, "2>&1 is an output redirection (fd dup)")
	assert.True(t, p.Segments[1].AfterPipe)
	assert.Equal(t, []string{"out.txt"}, p.Segments[1].Redirects)
	assert.Equal(t, []bool{true}, p.Segments[1].RedirectOut)
	assert.Equal(t, []string{"in.txt"}, p.Segments[2].Redirects)
	assert.Equal(t, []bool{false}, p.Segments[2].RedirectOut)
	assert.True(t, p.Segments[6].Heredoc, "<<< here-string")
	assert.Equal(t, []bool{true, true}, p.Segments[7].RedirectOut, "<> and &>> write")
}

func TestParseShellSegments_HeredocAndErrors(t *testing.T) {
	p, err := ParseShellSegments("cat <<EOF\n$(rm -rf /x)\nEOF")
	require.NoError(t, err)
	require.Len(t, p.Segments, 1)
	assert.True(t, p.Segments[0].Heredoc)
	assert.NotEmpty(t, p.Untokenized, "unquoted heredoc body with a substitution is untokenized code")

	p, err = ParseShellSegments("cat <<'EOF'\n$(rm -rf /x)\nEOF")
	require.NoError(t, err)
	assert.Empty(t, p.Untokenized)
	assert.False(t, p.CommandSubstitution)

	for _, s := range []string{"echo 'x", `echo "x`, "ls $(", "ls ("} {
		_, err := ParseShellSegments(s)
		assert.Error(t, err, s)
	}
}
