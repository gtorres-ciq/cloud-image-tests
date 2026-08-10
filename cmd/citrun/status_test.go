// cmd/citrun/status_test.go
package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderStatusAndTicker(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	st := NewRunState("r", now, []Job{
		{ID: "a", CPUCost: 16, BudgetKey: "C3_CPUS", Est: Duration(5 * time.Minute)},
		{ID: "b", CPUCost: 16, BudgetKey: "C3_CPUS", Est: Duration(5 * time.Minute)},
		{ID: "c", Est: Duration(5 * time.Minute)},
	})
	st.Jobs["a"].Status = StatusRunning
	st.Jobs["a"].Attempts = []Attempt{{Zone: "europe-west1-b", Start: now.Add(-time.Minute), Result: "running"}}
	st.Jobs["b"].Status = StatusPassed
	st.Cooldowns["c3|us-central1"] = now.Add(90 * time.Second)

	s := RenderStatus(st, now)
	for _, want := range []string{"queued 1", "running 1", "passed 1", "europe-west1", "16", "c3|us-central1 90s", "ETA ~"} {
		if !strings.Contains(s, want) {
			t.Errorf("status missing %q:\n%s", want, s)
		}
	}
	line := RenderTicker(st, now)
	if strings.Contains(line, "\n") || !strings.Contains(line, "running 1") {
		t.Errorf("ticker wrong: %q", line)
	}
}
