package brain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenizeQuery(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected []string
	}{
		{"single word", "bourbon", []string{"bourbon"}},
		{"single word uppercase", "Bourbon", []string{"bourbon"}},
		{"natural language with stopwords", "what bourbon does Jeff drink", []string{"bourbon", "jeff", "drink"}},
		{"who question", "who passed away recently", []string{"passed", "away"}},
		{"heavy stopwords", "what does Jeff do for work at Disney", []string{"jeff", "work", "disney"}},
		{"delimiter slash", "Helm/Kustomize", []string{"helm", "kustomize"}},
		{"delimiter hyphen", "blue-green", []string{"blue", "green"}},
		{"delimiter underscore", "my_project", []string{"project"}},
		{"delimiter comma", "cats,dogs", []string{"cats", "dogs"}},
		{"mixed delimiters and stopwords", "what about the helm/kustomize-config", []string{"helm", "kustomize", "config"}},
		{"all stopwords fallback", "what is it", []string{"what", "is", "it"}},
		{"dedup", "helm helm", []string{"helm"}},
		{"empty query", "", nil},
		{"only whitespace", "   ", nil},
		{"compound query", "family compound real estate", []string{"family", "compound", "real", "estate"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TokenizeQuery(tt.query)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSplitOnDelimiters(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"hello", []string{"hello"}},
		{"a/b", []string{"a", "b"}},
		{"a-b-c", []string{"a", "b", "c"}},
		{"a_b", []string{"a", "b"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"a/b-c_d,e", []string{"a", "b", "c", "d", "e"}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, splitOnDelimiters(tt.input))
		})
	}
}

func TestStripStopwords(t *testing.T) {
	assert.Equal(t, []string{"bourbon", "jeff"}, stripStopwords([]string{"what", "bourbon", "does", "jeff"}))
	assert.Equal(t, []string(nil), stripStopwords([]string{"what", "is", "it"}))
	assert.Equal(t, []string{"hello"}, stripStopwords([]string{"hello"}))
}
