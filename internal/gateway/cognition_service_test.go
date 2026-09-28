package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/brain"
	"conduit/internal/reflection"
)

func TestCognitionService_ZeroValueDisabled(t *testing.T) {
	var c CognitionService
	if c.BrainEnabled() || c.ReflectionEnabled() {
		t.Fatal("zero CognitionService must report brain and reflection disabled")
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop on zero value: %v", err)
	}

	called := make(chan string, 2)
	c.Start(context.Background(),
		func(context.Context) { called <- "reflect" },
		func(context.Context) { called <- "beads" })
	select {
	case name := <-called:
		t.Fatalf("disabled cognition started %s loop", name)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCognitionService_StartAndStop(t *testing.T) {
	b, err := brain.New(filepath.Join(t.TempDir(), "brain.db"), brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	c := CognitionService{
		Brain:            b,
		SessionReflector: reflection.NewSessionReflector(reflection.NewStore(b.DB())),
	}
	if !c.BrainEnabled() || !c.ReflectionEnabled() {
		t.Fatal("expected brain and reflection enabled")
	}

	called := make(chan string, 2)
	c.Start(context.Background(),
		func(context.Context) { called <- "reflect" },
		func(context.Context) { called <- "beads" })
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case name := <-called:
			got[name] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("loops started: %v", got)
		}
	}

	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
