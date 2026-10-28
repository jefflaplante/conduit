// Package trie is a per-level MQTT topic-filter trie mapping subscription
// patterns ("a/+/c", "x/#", exact topics) to rule IDs. The MQTT event rule
// engine (internal/mqttevents) routes every inbound event through it, so
// match cost is proportional to topic depth rather than rule count.
//
// Concurrency: a Trie is not safe for concurrent mutation. The engine builds
// it once at construction and never mutates it afterwards, and Match only
// reads, so concurrent Match calls on a fully built trie are safe without a
// lock (in practice the paho callback is a single goroutine anyway).
package trie

import (
	"fmt"
	"sort"
	"strings"
)

// node is one trie level. children keys are exact level strings; plus and
// hash are the MQTT wildcards, which must occupy an entire level (4.7.3).
// A hash node is always a leaf because validation rejects "#" unless it is
// the final level.
type node struct {
	children map[string]*node
	plus     *node
	hash     *node
	rules    []string // rule IDs whose pattern terminates at this node
}

// Trie maps MQTT topic filters to rule IDs. See the package doc for the
// concurrency contract.
type Trie struct {
	root *node
}

// New returns an empty trie.
func New() *Trie { return &Trie{root: &node{}} }

// ValidateFilter reports whether pattern is a valid MQTT topic filter per
// 4.7.3: non-empty, "+" and "#" occupy an entire level, and "#" is the final
// level. Exported so config validation rejects exactly what Insert would.
func ValidateFilter(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("empty topic filter")
	}
	if err := validate(strings.Split(pattern, "/")); err != nil {
		return fmt.Errorf("invalid topic filter %q: %w", pattern, err)
	}
	return nil
}

func validate(levels []string) error {
	for i, lv := range levels {
		if strings.ContainsAny(lv, "#+") && len(lv) != 1 {
			return fmt.Errorf("wildcard must occupy an entire level, got %q", lv)
		}
		if lv == "#" && i != len(levels)-1 {
			return fmt.Errorf(`"#" must be the final level`)
		}
	}
	return nil
}

// Insert adds ruleID under pattern. Invalid patterns (see ValidateFilter)
// return an error and leave the trie unmodified. Duplicate ruleIDs are
// permitted, including at the same node; Match reports each ID once.
func (t *Trie) Insert(pattern, ruleID string) error {
	if err := ValidateFilter(pattern); err != nil {
		return err
	}
	n := t.root
	for _, lv := range strings.Split(pattern, "/") {
		var next *node
		switch lv {
		case "+":
			if n.plus == nil {
				n.plus = &node{}
			}
			next = n.plus
		case "#":
			if n.hash == nil {
				n.hash = &node{}
			}
			next = n.hash
		default:
			if n.children == nil {
				n.children = make(map[string]*node)
			}
			if n.children[lv] == nil {
				n.children[lv] = &node{}
			}
			next = n.children[lv]
		}
		n = next
	}
	n.rules = append(n.rules, ruleID)
	return nil
}

// Match returns the rule IDs of every inserted pattern that matches topic,
// deduplicated and sorted so callers get set semantics and a deterministic
// order. A nil slice means no match.
//
// Each level consults at most three children (exact, "+", "#"), so exact and
// terminal-"#" patterns cost O(k) in topic levels. Overlapping "+" branches
// fan out, bounded by the number of matching patterns m, giving O(k + m).
//
// Per MQTT 4.7.1: "#" includes the parent level ("a/#" matches "a") and "+"
// matches exactly one level, which may be empty. Per 4.7.2, a first-level
// wildcard does not match topics beginning with "$" (broker-internal topics
// such as "$SYS/..."), so a "#" rule cannot fire on them; "$SYS/#" still
// does. A zero-length topic name is a protocol violation (4.7.3) and matches
// nothing.
func (t *Trie) Match(topic string) []string {
	if topic == "" {
		return nil
	}
	var ids []string
	var walk func(n *node, levels []string)
	walk = func(n *node, levels []string) {
		if n.hash != nil {
			ids = append(ids, n.hash.rules...) // "#" absorbs remaining levels
		}
		if len(levels) == 0 {
			ids = append(ids, n.rules...)
			return
		}
		if c := n.children[levels[0]]; c != nil {
			walk(c, levels[1:])
		}
		if n.plus != nil {
			walk(n.plus, levels[1:])
		}
	}
	levels := strings.Split(topic, "/")
	if strings.HasPrefix(topic, "$") {
		if c := t.root.children[levels[0]]; c != nil {
			walk(c, levels[1:])
		}
	} else {
		walk(t.root, levels)
	}
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
