package core

import (
	"context"
	"fmt"
	"testing"

	"conduit/internal/tools/types"
)

type fakeREMRunner struct{ phases [][]string }

func (f *fakeREMRunner) RunREMCycle(ctx context.Context, phases []string, dryRun bool) (*types.REMCycleReport, error) {
	f.phases = append(f.phases, phases)
	return &types.REMCycleReport{}, nil
}

// conduit-31jg.54: the default rem_cycle (what the nightly REM job runs) must
// include the Reflect phase — it clusters tool outcomes, promotes SPAR
// patterns into LTM and marks brain_reflections processed so grooming can
// trim them. "reflect" is accepted as an explicit phase too.
func TestBrainTool_REMCycleDefaultIncludesReflect(t *testing.T) {
	runner := &fakeREMRunner{}
	tool := NewBrainTool(&types.ToolServices{Brain: &fakeBrainService{}, REMCycle: runner})

	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "rem_cycle"})
	if err != nil || !res.Success {
		t.Fatalf("rem_cycle: err=%v res=%+v", err, res)
	}
	res, err = tool.Execute(context.Background(), map[string]interface{}{
		"action": "rem_cycle", "phases": []interface{}{"Reflect", "prune"},
	})
	if err != nil || !res.Success {
		t.Fatalf("rem_cycle explicit: err=%v res=%+v", err, res)
	}
	want := [][]string{
		{"triage", "reflect", "consolidation", "pruning", "integration", "grooming"},
		{"reflect", "pruning"},
	}
	if fmt.Sprint(runner.phases) != fmt.Sprint(want) {
		t.Fatalf("phases = %v, want %v", runner.phases, want)
	}
}
