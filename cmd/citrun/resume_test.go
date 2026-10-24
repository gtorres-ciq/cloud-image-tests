// cmd/citrun/resume_test.go
package main

import (
	"fmt"
	"testing"
	"time"
)

type fakeChecker struct {
	exists map[string]bool
	attach map[string]Result
}

func (f *fakeChecker) ContainerExists(jobID string) bool { return f.exists[jobID] }
func (f *fakeChecker) Attach(job Job, jobDir string) Result {
	return f.attach[job.ID]
}

func TestPrepareResume(t *testing.T) {
	now := time.Now()
	jobs := []Job{{ID: "passed"}, {ID: "queued"}, {ID: "stale"}, {ID: "alive"}, {ID: "infra"}, {ID: "real"}}
	st := NewRunState("r", now, jobs)
	st.Jobs["passed"].Status = StatusPassed
	st.Jobs["stale"].Status = StatusRunning
	st.Jobs["stale"].Attempts = []Attempt{{Zone: "z", Start: now, Result: "running"}}
	st.Jobs["alive"].Status = StatusRunning
	st.Jobs["alive"].Attempts = []Attempt{{Zone: "z", Start: now, Result: "running"}}
	st.Jobs["infra"].Status = StatusFailedInfra
	st.Jobs["real"].Status = StatusFailedReal

	chk := &fakeChecker{
		exists: map[string]bool{"alive": true},
		attach: map[string]Result{"alive": {Classification: ClassPass}},
	}
	requeued := prepareResume(&Scheduler{st: st, clock: realClock{}}, chk, t.TempDir(), false)

	if st.Jobs["passed"].Status != StatusPassed {
		t.Errorf("passed touched")
	}
	if st.Jobs["queued"].Status != StatusQueued {
		t.Errorf("queued changed")
	}
	if st.Jobs["stale"].Status != StatusQueued || st.Jobs["stale"].Attempts[0].Result != "orphaned" {
		t.Errorf("stale not orphan-requeued: %+v", st.Jobs["stale"])
	}
	if st.Jobs["alive"].Status != StatusPassed {
		t.Errorf("alive container's result not attached: %s", st.Jobs["alive"].Status)
	}
	if st.Jobs["infra"].Status != StatusQueued {
		t.Errorf("failed_infra should requeue on resume: %s", st.Jobs["infra"].Status)
	}
	if st.Jobs["real"].Status != StatusFailedReal {
		t.Errorf("failed_real must NOT requeue on plain resume")
	}
	if requeued != 3 { // queued + stale + infra
		t.Errorf("requeued = %d, want 3", requeued)
	}

	prepareResume(&Scheduler{st: st, clock: realClock{}}, chk, t.TempDir(), true)
	if st.Jobs["real"].Status != StatusQueued {
		t.Errorf("rerun-failed must requeue failed_real")
	}
}

func TestDryRunResumePlan(t *testing.T) {
	now := time.Now()
	jobs := []Job{{ID: "passed"}, {ID: "queued"}, {ID: "stale"}, {ID: "alive"}, {ID: "infra"}, {ID: "real"}}
	st := NewRunState("r", now, jobs)
	st.Jobs["passed"].Status = StatusPassed
	st.Jobs["stale"].Status = StatusRunning
	st.Jobs["stale"].Attempts = []Attempt{{Zone: "z", Start: now, Result: "running"}}
	st.Jobs["alive"].Status = StatusRunning
	st.Jobs["alive"].Attempts = []Attempt{{Zone: "z", Start: now, Result: "running"}}
	st.Jobs["infra"].Status = StatusFailedInfra
	st.Jobs["real"].Status = StatusFailedReal
	snapshot := func() map[string]JobStatus {
		m := map[string]JobStatus{}
		for id, rec := range st.Jobs {
			m[id] = rec.Status
		}
		return m
	}
	before := snapshot()

	chk := &fakeChecker{
		exists: map[string]bool{"alive": true},
		attach: map[string]Result{"alive": {Classification: ClassPass}},
	}

	requeue, attach, infra, orphans := dryRunResumePlan(st, chk, false)
	wantRequeue := []string{"queued", "stale", "infra"}
	if len(requeue) != len(wantRequeue) {
		t.Fatalf("requeue = %v, want %v", requeue, wantRequeue)
	}
	for i, id := range wantRequeue {
		if requeue[i] != id {
			t.Errorf("requeue[%d] = %s, want %s", i, requeue[i], id)
		}
	}
	if len(attach) != 1 || attach[0] != "alive" {
		t.Errorf("attach = %v, want [alive]", attach)
	}
	if infra != 1 || orphans != 1 {
		t.Errorf("infra/orphan counts = %d/%d, want 1/1", infra, orphans)
	}
	if after := snapshot(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("dry-run mutated state: before %v, after %v", before, after)
	}

	requeue, attach, infra, orphans = dryRunResumePlan(st, chk, true)
	wantRequeue = []string{"queued", "stale", "infra", "real"}
	if len(requeue) != len(wantRequeue) {
		t.Fatalf("onlyFailed requeue = %v, want %v", requeue, wantRequeue)
	}
	for i, id := range wantRequeue {
		if requeue[i] != id {
			t.Errorf("onlyFailed requeue[%d] = %s, want %s", i, requeue[i], id)
		}
	}
	if len(attach) != 1 || attach[0] != "alive" || infra != 1 || orphans != 1 {
		t.Errorf("onlyFailed attach/infra/orphans = %v/%d/%d, want [alive]/1/1", attach, infra, orphans)
	}
	if after := snapshot(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("dry-run (onlyFailed) mutated state: before %v, after %v", before, after)
	}
}
