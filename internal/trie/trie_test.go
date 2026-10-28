package trie

import (
	"reflect"
	"testing"
)

func TestMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern []string // (pattern, ruleID) pairs inserted in order
		topic   string
		want    []string
	}{
		// Exact matches.
		{
			name:    "exact hit",
			pattern: []string{"devices/bridge/state", "bridge-down"},
			topic:   "devices/bridge/state",
			want:    []string{"bridge-down"},
		},
		{
			name:    "exact miss on last level",
			pattern: []string{"a/b/c", "r1"},
			topic:   "a/b/d",
			want:    nil,
		},
		{
			name:    "exact miss on first level",
			pattern: []string{"a/b/c", "r1"},
			topic:   "x/b/c",
			want:    nil,
		},
		// '+' at each position.
		{
			name:    "plus leading",
			pattern: []string{"+/bridge/state", "r1"},
			topic:   "devices/bridge/state",
			want:    []string{"r1"},
		},
		{
			name:    "plus middle",
			pattern: []string{"devices/+/state", "r1"},
			topic:   "devices/bridge/state",
			want:    []string{"r1"},
		},
		{
			name:    "plus trailing",
			pattern: []string{"devices/bridge/+", "r1"},
			topic:   "devices/bridge/state",
			want:    []string{"r1"},
		},
		{
			name:    "plus matches exactly one level not two",
			pattern: []string{"a/+", "r1"},
			topic:   "a/b/c",
			want:    nil,
		},
		// '#' terminal.
		{
			name:    "hash matches deep topic",
			pattern: []string{"devices/#", "r1"},
			topic:   "devices/room/plug1/state",
			want:    []string{"r1"},
		},
		{
			name:    "hash matches parent level per 4.7.1",
			pattern: []string{"a/#", "r1"},
			topic:   "a",
			want:    []string{"r1"},
		},
		{
			name:    "hash does not cross first level",
			pattern: []string{"a/#", "r1"},
			topic:   "b/c",
			want:    nil,
		},
		// Overlapping patterns -> multiple rule IDs, sorted.
		{
			name: "overlapping patterns return all matching rules sorted",
			pattern: []string{
				"sensors/total/battery/state", "exact-rule",
				"sensors/+/battery/state", "mid-rule",
				"sensors/#", "tree-rule",
			},
			topic: "sensors/total/battery/state",
			want:  []string{"exact-rule", "mid-rule", "tree-rule"},
		},
		// Dedup: one rule ID reachable via two matching branches.
		{
			name:    "duplicate rule id via two branches reported once",
			pattern: []string{"a/b", "dup", "a/+", "dup"},
			topic:   "a/b",
			want:    []string{"dup"},
		},
		{
			name:    "duplicate rule id in one branch appended twice",
			pattern: []string{"a/#", "dup", "a/#", "dup"},
			topic:   "a/b/c",
			want:    []string{"dup"},
		},
		// MQTT 4.7 edge: empty level.
		{
			name:    "plus matches empty level per 4.7.1.2",
			pattern: []string{"a/+/c", "r1"},
			topic:   "a//c",
			want:    []string{"r1"},
		},
		{
			name:    "exact pattern matches empty level",
			pattern: []string{"a//c", "r1"},
			topic:   "a//c",
			want:    []string{"r1"},
		},
		{
			name:    "hash absorbs empty levels",
			pattern: []string{"a/#", "r1"},
			topic:   "a//x",
			want:    []string{"r1"},
		},
		// MQTT 4.7 edge: filter shorter/longer than topic.
		{
			name:    "filter shorter than topic without hash does not match",
			pattern: []string{"a/b", "r1"},
			topic:   "a/b/c",
			want:    nil,
		},
		{
			name:    "filter longer than topic does not match",
			pattern: []string{"a/b/c", "r1"},
			topic:   "a/b",
			want:    nil,
		},
		// Protocol-level edges.
		{
			name:    "empty topic matches nothing",
			pattern: []string{"#", "r1"},
			topic:   "",
			want:    nil,
		},
		{
			name:    "root-level hash matches any single-segment topic",
			pattern: []string{"#", "r1"},
			topic:   "anything",
			want:    []string{"r1"},
		},
		// MQTT 4.7.2: first-level wildcards skip "$" topics.
		{
			name:    "root hash does not match dollar topic",
			pattern: []string{"#", "r1", "+/broker/load", "r2"},
			topic:   "$SYS/broker/load",
			want:    nil,
		},
		{
			name:    "explicit dollar prefix with wildcards matches",
			pattern: []string{"$SYS/#", "r1", "$SYS/+/load", "r2"},
			topic:   "$SYS/broker/load",
			want:    []string{"r1", "r2"},
		},
		{
			name:    "dollar below the first level is an ordinary level",
			pattern: []string{"a/#", "r1", "a/+", "r2"},
			topic:   "a/$x",
			want:    []string{"r1", "r2"},
		},
		{
			name:    "empty trie matches nothing",
			pattern: nil,
			topic:   "a/b",
			want:    nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := New()
			for i := 0; i+1 < len(tc.pattern); i += 2 {
				if err := tr.Insert(tc.pattern[i], tc.pattern[i+1]); err != nil {
					t.Fatalf("Insert(%q, %q): %v", tc.pattern[i], tc.pattern[i+1], err)
				}
			}
			got := tr.Match(tc.topic)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Match(%q) = %v, want %v", tc.topic, got, tc.want)
			}
		})
	}
}

func TestInsert(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{name: "exact pattern accepted", pattern: "a/b/c"},
		{name: "plus pattern accepted", pattern: "a/+/c"},
		{name: "terminal hash accepted", pattern: "a/#"},
		{name: "bare hash accepted", pattern: "#"},
		{name: "bare plus accepted", pattern: "+"},
		{name: "empty pattern rejected", pattern: "", wantErr: true},
		// MQTT 4.7.3: wildcards must occupy an entire level.
		{name: "hash mid pattern rejected 4.7.3", pattern: "a/#/c", wantErr: true},
		{name: "leading hash rejected 4.7.3", pattern: "#/a", wantErr: true},
		{name: "plus embedded in level rejected 4.7.3", pattern: "a+/b", wantErr: true},
		{name: "hash embedded in level rejected 4.7.3", pattern: "a/b#/c", wantErr: true},
		{name: "trailing embedded hash rejected 4.7.3", pattern: "a/b#", wantErr: true},
		{name: "trailing embedded plus rejected 4.7.3", pattern: "a/b+", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := New()
			err := tr.Insert(tc.pattern, "r1")
			if (err != nil) != tc.wantErr {
				t.Errorf("Insert(%q) error = %v, wantErr %v", tc.pattern, err, tc.wantErr)
			}
			// ValidateFilter is the same check config validation uses, so
			// it must agree with Insert on every pattern.
			if verr := ValidateFilter(tc.pattern); (verr != nil) != tc.wantErr {
				t.Errorf("ValidateFilter(%q) error = %v, wantErr %v", tc.pattern, verr, tc.wantErr)
			}
		})
	}
}

func TestInsertRejectedPatternLeavesTrieUnchanged(t *testing.T) {
	t.Parallel()
	tr := New()
	if err := tr.Insert("a/#/b", "bad"); err == nil {
		t.Fatal("Insert(a/#/b) succeeded, want error")
	}
	if got := tr.Match("a/x/b"); got != nil {
		t.Errorf("Match after rejected insert = %v, want nil (trie must be unmodified)", got)
	}
}

func TestMatchOrderDeterministic(t *testing.T) {
	t.Parallel()
	build := func() []string {
		tr := New()
		for _, p := range [][2]string{
			{"zzz/#", "rule-c"},
			{"a/+", "rule-a"},
			{"a/b", "rule-b"},
		} {
			if err := tr.Insert(p[0], p[1]); err != nil {
				t.Fatalf("Insert(%q): %v", p[0], err)
			}
		}
		return tr.Match("a/b")
	}
	first := build()
	for i := 0; i < 20; i++ {
		if got := build(); !reflect.DeepEqual(got, first) {
			t.Fatalf("Match order not deterministic: got %v, want %v", got, first)
		}
	}
	want := []string{"rule-a", "rule-b"} // sorted, not insertion order
	if !reflect.DeepEqual(first, want) {
		t.Errorf("Match = %v, want %v", first, want)
	}
}
