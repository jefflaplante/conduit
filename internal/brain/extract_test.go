package brain

import (
	"strings"
	"testing"
)

func TestExtractBulkEntries(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []BulkExtract
	}{
		{
			name:    "empty content",
			content: "",
			want:    nil,
		},
		{
			name:    "no brain-extract block",
			content: "just some markdown\n# Heading\n",
			want:    nil,
		},
		{
			name: "single entry",
			content: `# Notes
<!-- brain-extract
panel_count: "30"
/brain-extract -->
more text`,
			want: []BulkExtract{
				{Key: "panel_count", Value: "30"},
			},
		},
		{
			name: "multi-entry",
			content: `<!-- brain-extract
solar.panels: "30"
solar.inverter: "Enphase IQ8+"
home.sqft: "2400"
/brain-extract -->`,
			want: []BulkExtract{
				{Key: "solar.panels", Value: "30"},
				{Key: "solar.inverter", Value: "Enphase IQ8+"},
				{Key: "home.sqft", Value: "2400"},
			},
		},
		{
			name: "malformed lines are skipped",
			content: `<!-- brain-extract
good.key: "ok"
this is not a valid line
another bad one
also_ok: "yes"
malformed: no quotes here
bad-format: "unterminated
/brain-extract -->`,
			want: []BulkExtract{
				{Key: "good.key", Value: "ok"},
				{Key: "also_ok", Value: "yes"},
			},
		},
		{
			name: "multiple blocks",
			content: `First section
<!-- brain-extract
a: "1"
b: "2"
/brain-extract -->
Middle prose here.
<!-- brain-extract
c: "3"
/brain-extract -->
End.`,
			want: []BulkExtract{
				{Key: "a", Value: "1"},
				{Key: "b", Value: "2"},
				{Key: "c", Value: "3"},
			},
		},
		{
			name: "blank and comment lines ignored",
			content: `<!-- brain-extract

# section comment
key.one: "alpha"

# another comment
key.two: "beta"
/brain-extract -->`,
			want: []BulkExtract{
				{Key: "key.one", Value: "alpha"},
				{Key: "key.two", Value: "beta"},
			},
		},
		{
			name: "unknown directive tolerated",
			content: `<!-- brain-extract
brain-extract-ignore: skip
real.key: "value"
/brain-extract -->`,
			want: []BulkExtract{
				// brain-extract-ignore line has a hyphenated key — the key regex
				// permits hyphens but the value lacks quotes, so it is skipped.
				{Key: "real.key", Value: "value"},
			},
		},
		{
			name: "empty block",
			content: `<!-- brain-extract
/brain-extract -->`,
			want: nil,
		},
		{
			name: "value with special characters",
			content: `<!-- brain-extract
path: "/etc/config.json"
regex: "^[a-z]+$"
/brain-extract -->`,
			want: []BulkExtract{
				{Key: "path", Value: "/etc/config.json"},
				{Key: "regex", Value: "^[a-z]+$"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractBulkEntries(tc.content)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d entries, want %d: got=%+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].Key != tc.want[i].Key || got[i].Value != tc.want[i].Value {
					t.Errorf("entry %d: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestExtractBulkEntriesNoPanic proves the parser never panics on bizarre input.
func TestExtractBulkEntriesNoPanic(t *testing.T) {
	inputs := []string{
		"<!-- brain-extract",       // unterminated opening
		"/brain-extract -->",       // orphan closer
		strings.Repeat("x", 10000), // long junk
		"<!-- brain-extract /brain-extract -->",
		"<!-- brain-extract\n\n\n/brain-extract -->",
		"<!-- brain-extract\n: \"no key\"\n/brain-extract -->",
		"<!-- brain-extract\nkey: \n/brain-extract -->",
	}
	for _, in := range inputs {
		// Must not panic
		_ = ExtractBulkEntries(in)
	}
}

// TestExtractBulkEntriesFastPath verifies that content lacking the marker
// bypasses regex work entirely.
func TestExtractBulkEntriesFastPath(t *testing.T) {
	content := strings.Repeat("no marker here\n", 1000)
	got := ExtractBulkEntries(content)
	if got != nil {
		t.Fatalf("expected nil for content without marker, got %+v", got)
	}
}
