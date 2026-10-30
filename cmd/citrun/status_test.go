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
		{ID: "a", Shape: "c3-standard-4", CPUCost: 16, BudgetKey: "C3_CPUS",
			Zones: []string{"europe-west1-b", "europe-west1-c"}, Est: Duration(5 * time.Minute)},
		{ID: "b", Shape: "n1-standard-4", CPUCost: 16, BudgetKey: "C3_CPUS",
			Zones: []string{"europe-west1-b"}, Est: Duration(5 * time.Minute)},
		{ID: "c", Shape: "n1-standard-4", CPUCost: 4, BudgetKey: "C3_CPUS",
			Zones: []string{"europe-west4-a"}, Est: Duration(5 * time.Minute)},
	})
	st.Jobs["a"].Status = StatusRunning
	st.Jobs["a"].Attempts = []Attempt{{Zone: "europe-west1-b", Start: now.Add(-time.Minute), Result: "running"}}
	st.Jobs["b"].Status = StatusPassed
	st.Cooldowns["c3|us-central1"] = now.Add(90 * time.Second)

	// europe-west1: CPUS 20, C3_CPUS 10 → usable CPUS 16, C3_CPUS 8. Jobs a/b
	// (16 vCPU) fit CPUS but not C3_CPUS; europe-west1 zones are excluded for
	// them (deduped across zones and images). c fits europe-west4 and must
	// NOT appear — busy-but-feasible zones are never listed.
	cfg := &Config{SafetyFactor: 0.8, Budgets: Budgets{Regions: map[string]map[string]int{
		"europe-west1": {"CPUS": 20, "C3_CPUS": 10},
		"europe-west4": {"CPUS": 100, "C3_CPUS": 100},
	}}}

	s := RenderStatus(cfg, st, now)
	for _, want := range []string{
		"queued 1", "running 1", "passed 1", "europe-west1", "16", "c3|us-central1 90s", "ETA ~",
		"excluded zones (never budget-feasible; skipped by sweeps):",
		"c3-standard-4 europe-west1-b: needs 16 vCPU; usable C3_CPUS=8, CPUS=16",
		"c3-standard-4 europe-west1-c: needs 16 vCPU; usable C3_CPUS=8, CPUS=16",
		"n1-standard-4 europe-west1-b: needs 16 vCPU; usable C3_CPUS=8, CPUS=16",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("status missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "europe-west4-a") {
		t.Errorf("feasible zone listed as excluded:\n%s", s)
	}
	line := RenderTicker(st, now)
	if strings.Contains(line, "\n") || !strings.Contains(line, "running 1") {
		t.Errorf("ticker wrong: %q", line)
	}

	// nil config (old run dir without citrun.yaml) skips the section.
	s = RenderStatus(nil, st, now)
	if strings.Contains(s, "excluded zones") {
		t.Errorf("nil config must skip excluded-zones section:\n%s", s)
	}
}
