package eval

import (
	"testing"
)

// TestTestdataCorpusLoads guards the in-repo synthetic corpus: every case
// must parse and pass its own rubric in pure replay (identity replay).
// This is the CI canary for the corpus format.
func TestTestdataCorpusLoads(t *testing.T) {
	cases, err := LoadCorpus("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatalf("corpus has %d cases, want >= 10", len(cases))
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.ID] {
			t.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
		if c.Mode != "replay" {
			t.Errorf("case %s: mode = %q, want replay", c.ID, c.Mode)
		}
		if len(c.Rubric) == 0 {
			t.Errorf("case %s: empty rubric", c.ID)
		}
	}
}

func TestTestdataCorpusReplayPasses(t *testing.T) {
	cases, err := LoadCorpus("testdata")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(cases, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 {
		for _, r := range res.Results {
			if !r.Passed {
				t.Errorf("case %s failed: %+v", r.CaseID, r.Checks)
			}
		}
		t.Fatalf("corpus should be self-consistent: %d failures", res.Failed)
	}
	if res.Total != len(cases) {
		t.Fatalf("total = %d, want %d", res.Total, len(cases))
	}
}
