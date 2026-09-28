package monitoring

import (
	"sync"
	"testing"
	"time"
)

func TestConfigUpdateMetrics_RecordAndSnapshot(t *testing.T) {
	var m ConfigUpdateMetrics
	if s := m.Snapshot(); s.LastApplied != nil || s.ToMap()["last_applied"] != nil {
		t.Fatalf("zero snapshot = %+v", s)
	}
	for _, o := range ConfigUpdateOutcomes {
		m.Record(o)
	}
	m.Record("bogus") // ignored
	at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	m.RecordApplied(ConfigUpdateAppliedLive, 2, at)

	s := m.Snapshot()
	for _, o := range ConfigUpdateOutcomes {
		want := int64(1)
		if o == ConfigUpdateAppliedLive {
			want = 2
		}
		if got := s.Count(o); got != want {
			t.Errorf("%s = %d, want %d", o, got, want)
		}
		if got := s.ToMap()[string(o)+"_total"]; got != want {
			t.Errorf("map %s = %v, want %d", o, got, want)
		}
	}
	if s.ProvidersRebuilt != 2 || s.LastApplied == nil || !s.LastApplied.Equal(at) {
		t.Errorf("snapshot = %+v", s)
	}
	if got := s.ToMap()["last_applied"]; got != "2026-09-28T12:00:00Z" {
		t.Errorf("last_applied = %v", got)
	}
}

func TestConfigUpdateMetrics_Concurrent(t *testing.T) {
	var m ConfigUpdateMetrics
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.RecordApplied(ConfigUpdateAppliedRestartRequired, 1, time.Now())
				m.Record(ConfigUpdateFailed)
				_ = m.Snapshot()
			}
		}()
	}
	wg.Wait()
	s := m.Snapshot()
	if s.AppliedRestartRequired != 800 || s.Failed != 800 || s.ProvidersRebuilt != 800 {
		t.Errorf("snapshot = %+v", s)
	}
}
