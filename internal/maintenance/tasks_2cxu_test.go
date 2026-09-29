package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// orderTask records the order tasks ran in.
type orderTask struct {
	TestTask
	ran *[]string
}

func (o *orderTask) Execute(ctx context.Context) TaskResult {
	*o.ran = append(*o.ran, o.name)
	return TaskResult{Success: true}
}

// conduit-2cxu: RunNow runs tasks in registration order (session cleanup
// before VACUUM), not map order.
func TestScheduler_RunNowInRegistrationOrder(t *testing.T) {
	s := NewScheduler(nil, DefaultConfig(), log.New(io.Discard, "", 0))
	var ran []string
	names := []string{"session_cleanup", "database_maintenance", "c", "d", "e"}
	for _, n := range names {
		if err := s.RegisterTask(&orderTask{TestTask: TestTask{name: n}, ran: &ran}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		ran = nil
		if err := s.RunNow(context.Background()); err != nil {
			t.Fatal(err)
		}
		if strings.Join(ran, ",") != strings.Join(names, ",") {
			t.Fatalf("run order %v, want %v", ran, names)
		}
	}
	if got := strings.Join(s.TaskNames(), ","); got != strings.Join(names, ",") {
		t.Fatalf("TaskNames = %s", got)
	}
}

// conduit-2cxu: the pre-VACUUM backup is a 0600 VACUUM INTO copy, and a dry
// run changes nothing.
func TestDatabaseMaintenanceTask_BackupAndDryRun(t *testing.T) {
	db, path := openGatewayDB(t)
	cfg := DatabaseConfig{VacuumEnabled: true, VacuumThreshold: -1, BackupBeforeVacuum: true, OptimizeIndexes: true, DryRun: true}
	task := NewDatabaseMaintenanceTask(db, path, cfg, log.New(io.Discard, "", 0))
	res := task.Execute(context.Background())
	if !res.Success || !strings.Contains(res.Message, "would run backup, VACUUM, ANALYZE") {
		t.Fatalf("dry run = %+v", res)
	}
	if b, _ := filepath.Glob(path + ".backup.*"); len(b) != 0 {
		t.Fatalf("dry run wrote %v", b)
	}

	cfg.DryRun = false
	task = NewDatabaseMaintenanceTask(db, path, cfg, log.New(io.Discard, "", 0))
	if res := task.Execute(context.Background()); !res.Success {
		t.Fatalf("run = %+v", res)
	}
	b, _ := filepath.Glob(path + ".backup.*")
	if len(b) != 1 {
		t.Fatalf("backups = %v", b)
	}
	st, err := os.Stat(b[0])
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v, want 0600", st.Mode().Perm())
	}
}

func TestTaskResult_JSONError(t *testing.T) {
	raw, err := json.Marshal(TaskResult{Success: false, Message: "m", Error: errors.New("boom")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"error":"boom"`) {
		t.Fatalf("json = %s", raw)
	}
}
