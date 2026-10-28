package trie

import (
	"fmt"
	"testing"
)

// benchTrie builds 1000 rules over typical home-automation topic shapes:
// 800 exact sensor topics, 100 '+'-heavy patterns and 100 '#' subtrees.
func benchTrie(b *testing.B) *Trie {
	tr := New()
	for i := 0; i < 800; i++ {
		pat := fmt.Sprintf("sensors/inverter_%d/total_%d/state", i%8, i)
		if err := tr.Insert(pat, fmt.Sprintf("exact-%d", i)); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		pat := fmt.Sprintf("devices/room_%d/+/state", i%10)
		if err := tr.Insert(pat, fmt.Sprintf("plus-%d", i)); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		pat := fmt.Sprintf("devices/plug_%d/#", i)
		if err := tr.Insert(pat, fmt.Sprintf("hash-%d", i)); err != nil {
			b.Fatal(err)
		}
	}
	return tr
}

func BenchmarkMatchMixed1000Rules(b *testing.B) {
	tr := benchTrie(b)
	// One exact rule matches; the walk still has to consult the '+' and '#'
	// branches at every level, which is the cost being measured.
	topic := "sensors/inverter_1/total_257/state" // 257%8 == 1
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := tr.Match(topic); len(got) != 1 {
			b.Fatalf("Match = %v, want exactly 1 rule", got)
		}
	}
}
