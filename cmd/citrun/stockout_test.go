package main

import (
	"context"
	"testing"
	"time"
)

// completeAttempt exercises accounting and completion without executor goroutines.
func completeAttempt(s *Scheduler, j Job, zone, class string) {
	s.jobCtx = context.Background()
	s.charge(j, zoneRegion(zone), 1)
	rec := s.st.Jobs[j.ID]
	rec.Status = StatusRunning
	rec.Attempts = append(rec.Attempts, Attempt{Zone: zone, Start: s.clock.Now(), Result: "running"})
	s.handleCompletion(completion{jobID: j.ID, zone: zone, start: s.clock.Now(), end: s.clock.Now(), res: Result{Classification: class}})
}

func TestStockoutSweepsZonesBeforeWaiting(t *testing.T) {
	j := job("j", 16)
	j.Shape = "c3-standard-4"
	j.Zones = []string{"r1-a", "r1-b", "r2-a"}
	s, _, clk, st := schedFixture(t, schedConfig, []Job{j})
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		z := s.pickZone(j, avoidZone(st.Jobs[j.ID]))
		if z == "" || seen[z] {
			t.Fatalf("placement %d = %q, already tried %v; must exhaust zones without waiting", i, z, seen)
		}
		seen[z] = true
		completeAttempt(s, j, z, ClassStockout)
	}
	if z := s.pickZone(j, avoidZone(st.Jobs[j.ID])); z != "" {
		t.Fatalf("exhausted sweep launched in %s before cooldown", z)
	}
	clk.Advance(121 * time.Second)
	if z := s.pickZone(j, avoidZone(st.Jobs[j.ID])); z == "" {
		t.Fatal("exhausted sweep never resumed")
	}
}

func TestStockoutDoesNotBlockOtherShapes(t *testing.T) {
	metal := job("metal", 16)
	metal.Shape = "c3-standard-192-metal"
	metal.Zones = []string{"r1-a"}
	regular := job("regular", 16)
	regular.Shape = "c3-standard-4"
	regular.Zones = []string{"r1-a"}
	s, _, _, _ := schedFixture(t, schedConfig, []Job{metal, regular})
	completeAttempt(s, metal, "r1-a", ClassStockout)
	if z := s.pickZone(regular, ""); z != "r1-a" {
		t.Fatalf("metal stockout blocked ordinary C3: %q", z)
	}
}

func TestStockoutSweepSurvivesResume(t *testing.T) {
	j := job("j", 16)
	j.Zones = []string{"r1-a", "r1-b"}
	s, ex, clk, st := schedFixture(t, schedConfig, []Job{j})
	completeAttempt(s, j, "r1-a", ClassStockout)
	loaded, err := LoadState(s.runDir)
	if err != nil {
		t.Fatal(err)
	}
	resumed := NewScheduler(s.cfg, loaded, ex, clk, s.runDir)
	if z := resumed.pickZone(j, avoidZone(st.Jobs[j.ID])); z != "r1-b" {
		t.Fatalf("resume lost untried zone: %q", z)
	}
	completeAttempt(resumed, j, "r1-b", ClassStockout)
	loaded, err = LoadState(s.runDir)
	if err != nil {
		t.Fatal(err)
	}
	resumed = NewScheduler(s.cfg, loaded, ex, clk, s.runDir)
	if z := resumed.pickZone(j, ""); z != "" {
		t.Fatalf("resume bypassed exhausted sweep cooldown: %q", z)
	}
	clk.Advance(121 * time.Second)
	if z := resumed.pickZone(j, ""); z == "" {
		t.Fatal("resume stayed blocked after cooldown")
	}
}

func TestStockoutTriesAllZonesBeforeRetryCap(t *testing.T) {
	j := job("j", 16)
	j.Zones = []string{"r1-a", "r1-b", "r1-c", "r2-a", "r2-b"}
	s, _, _, st := schedFixture(t, schedConfig, []Job{j})
	for _, z := range j.Zones[:4] {
		completeAttempt(s, j, z, ClassStockout)
	}
	if st.Jobs[j.ID].Status != StatusQueued {
		t.Fatal("attempt cap prevented trying fifth eligible zone")
	}
	if z := s.pickZone(j, avoidZone(st.Jobs[j.ID])); z != "r2-b" {
		t.Fatalf("next zone = %q, want untried r2-b", z)
	}
}

func TestStockoutOnlyCountsZonesThatFitBudgets(t *testing.T) {
	j := job("j", 16)
	j.Zones = []string{"r1-a", "r2-a"}
	s, _, clk, st := schedFixture(t, schedConfig, []Job{j})
	s.cfg.Budgets.Regions["r2"]["C3_CPUS"] = 1
	completeAttempt(s, j, "r1-a", ClassStockout)
	if z := s.pickZone(j, avoidZone(st.Jobs[j.ID])); z != "" {
		t.Fatalf("want cooldown, got %s", z)
	}
	clk.Advance(121 * time.Second)
	if z := s.pickZone(j, avoidZone(st.Jobs[j.ID])); z != "r1-a" {
		t.Fatalf("only fitting zone must be retried after cooldown, got %q", z)
	}
}

func TestQuotaStillBlocksWholeRegion(t *testing.T) {
	j := job("j", 16)
	j.Zones = []string{"r1-a", "r1-b", "r2-a"}
	s, _, _, st := schedFixture(t, schedConfig, []Job{j})
	completeAttempt(s, j, "r1-a", ClassQuota)
	if z := s.pickZone(j, avoidZone(st.Jobs[j.ID])); z != "r2-a" {
		t.Fatalf("quota retry must leave region, got %q", z)
	}
}

func TestStockoutCompletedSweepLimitAndResume(t *testing.T) {
	j := job("j", 16)
	s, _, clk, st := schedFixture(t, schedConfig, []Job{j})
	for round := 0; round < 4; round++ {
		for n := 0; n < 2; n++ {
			z := s.pickZone(j, avoidZone(st.Jobs[j.ID]))
			if z == "" {
				t.Fatalf("round %d attempt %d blocked", round, n)
			}
			completeAttempt(s, j, z, ClassStockout)
		}
		if round < 3 {
			if st.Jobs[j.ID].Status != StatusQueued {
				t.Fatal("failed before four complete sweeps")
			}
			clk.Advance(time.Hour)
		}
	}
	if st.Jobs[j.ID].Status != StatusFailedInfra {
		t.Fatal("unbounded stockout retries")
	}
	prepareResume(s, nil, s.runDir, false)
	if z := s.pickZone(j, ""); z == "" {
		t.Fatal("explicit resume cannot retry exhausted job")
	}
}

func TestStockoutBusyRegionIsNotExhausted(t *testing.T) {
	j := job("j", 16)
	s, _, _, st := schedFixture(t, schedConfig, []Job{j})
	s.usedCPU["r2"] = map[string]int{"CPUS": 32, "C3_CPUS": 32}
	completeAttempt(s, j, "r1-a", ClassStockout)
	if z := s.pickZone(j, ""); z != "" {
		t.Fatalf("launched into occupied region: %q", z)
	}
	s.usedCPU["r2"] = map[string]int{}
	if z := s.pickZone(j, avoidZone(st.Jobs[j.ID])); z != "r2-a" {
		t.Fatalf("job should launch as soon as occupancy frees: %q", z)
	}
}

func TestLegacyStockoutCooldownDoesNotBlockOtherShapes(t *testing.T) {
	j := job("j", 16)
	j.Zones = []string{"r1-a"}
	s, ex, clk, st := schedFixture(t, schedConfig, []Job{j})
	st.Cooldowns["c3|r1"] = clk.Now().Add(time.Hour)
	st.Jobs[j.ID].Attempts = []Attempt{{Zone: "r1-a", End: clk.Now(), Result: ClassStockout}}
	resumed := NewScheduler(s.cfg, st, ex, clk, s.runDir)
	if z := resumed.pickZone(j, ""); z != "r1-a" {
		t.Fatalf("legacy stockout still blocks new scheduler: %q", z)
	}
}

func TestLegacyQuotaCooldownSurvivesMigration(t *testing.T) {
	j := job("j", 16)
	s, ex, clk, st := schedFixture(t, schedConfig, []Job{j})
	st.Cooldowns["c3|r1"] = clk.Now().Add(time.Hour)
	st.Jobs[j.ID].Attempts = []Attempt{{Zone: "r1-a", End: clk.Now(), Result: ClassQuota}}
	resumed := NewScheduler(s.cfg, st, ex, clk, s.runDir)
	if z := resumed.pickZone(j, ""); z != "r2-a" {
		t.Fatalf("legacy quota no longer blocks region: %q", z)
	}
}

func TestConcurrentQuotaFailuresDoNotExtendCooldown(t *testing.T) {
	a, b := job("a", 16), job("b", 16)
	s, _, clk, st := schedFixture(t, schedConfig, []Job{a, b})
	completeAttempt(s, a, "r1-a", ClassQuota)
	clk.Advance(time.Minute)
	completeAttempt(s, b, "r1-a", ClassQuota)
	clk.Advance(61 * time.Second)
	// Both regions have enough room despite the 10-minute quota penalty.
	a.Zones = []string{"r1-a"}
	if z := s.pickZone(a, ""); z != "r1-a" {
		t.Fatalf("second in-flight failure extended cooldown: %q (%v)", z, st.Cooldowns)
	}
}

func TestResumeAttachedStockoutCompletesSweep(t *testing.T) {
	j := job("j", 16)
	s, _, clk, st := schedFixture(t, schedConfig, []Job{j})
	completeAttempt(s, j, "r1-a", ClassStockout)
	rec := st.Jobs[j.ID]
	rec.Status = StatusRunning
	rec.Attempts = append(rec.Attempts, Attempt{Zone: "r2-a", Start: clk.Now(), Result: "running"})
	chk := &fakeChecker{exists: map[string]bool{j.ID: true}, attach: map[string]Result{j.ID: {Classification: ClassStockout}}}
	prepareResume(s, chk, s.runDir, false)
	if z := s.pickZone(j, avoidZone(rec)); z != "" {
		t.Fatalf("attached stockout retried exhausted sweep immediately: %q", z)
	}
}
