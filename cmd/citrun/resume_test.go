// cmd/citrun/resume_test.go
package main

import (
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
	requeued := prepareResume(st, chk, t.TempDir(), false)

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

	prepareResume(st, chk, t.TempDir(), true)
	if st.Jobs["real"].Status != StatusQueued {
		t.Errorf("rerun-failed must requeue failed_real")
	}
}
