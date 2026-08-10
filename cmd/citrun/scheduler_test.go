// cmd/citrun/scheduler_test.go
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- fakes ----

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}
type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
}
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, fakeWaiter{c.now.Add(d), ch})
	return ch
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	var keep []fakeWaiter
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

// fakeExec returns scripted results per job (consumed in order); jobs listed
// in gate block until released.
type fakeExec struct {
	mu      sync.Mutex
	script  map[string][]Result
	calls   []string // "jobID@zone"
	gate    map[string]chan struct{}
	running int
	maxSeen int
}

func newFakeExec() *fakeExec {
	return &fakeExec{script: map[string][]Result{}, gate: map[string]chan struct{}{}}
}
func (f *fakeExec) Run(ctx context.Context, job Job, zone, jobDir string) Result {
	f.mu.Lock()
	f.calls = append(f.calls, job.ID+"@"+zone)
	f.running++
	if f.running > f.maxSeen {
		f.maxSeen = f.running
	}
	res := Result{Classification: ClassPass}
	if s := f.script[job.ID]; len(s) > 0 {
		res, f.script[job.ID] = s[0], s[1:]
	}
	g := f.gate[job.ID]
	f.mu.Unlock()
	if g != nil {
		<-g
	}
	f.mu.Lock()
	f.running--
	f.mu.Unlock()
	return res
}
func (f *fakeExec) callsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// ---- helpers ----

const schedConfig = `
project: p
image_prefix: pre
container_cap: 3
budgets:
  networks: 4
  subnetworks: 10
  regions:
    r1: {CPUS: 100, C3_CPUS: 40}
    r2: {CPUS: 100, C3_CPUS: 40}
zone_sets:
  both: [r1-a, r2-a]
  r1only: [r1-a]
shapes:
  c3-standard-4: {arch: x86, cpus: 4, family: C3_CPUS, zone_set: both}
suites:
  ssh: {vms: 4, timeout: 30m, est: 9m}
`

func schedFixture(t *testing.T, cfgYAML string, jobs []Job) (*Scheduler, *fakeExec, *fakeClock, *RunState) {
	t.Helper()
	cfg, err := LoadConfig(writeTemp(t, cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	clk := newFakeClock()
	st := NewRunState("t", clk.Now(), jobs)
	dir := t.TempDir()
	if err := SaveState(dir, st); err != nil {
		t.Fatal(err)
	}
	ex := newFakeExec()
	return NewScheduler(cfg, st, ex, clk, dir), ex, clk, st
}

func job(id string, cpu int) Job {
	return Job{ID: id, BudgetKey: "C3_CPUS", Series: "c3", CPUCost: cpu,
		Zones: []string{"r1-a", "r2-a"}, Timeout: Duration(30 * time.Minute)}
}

func runSched(t *testing.T, s *Scheduler) chan error {
	t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- s.Run(context.Background(), context.Background()) }()
	return errc
}

func waitDone(t *testing.T, errc chan error) {
	t.Helper()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("scheduler: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scheduler did not finish")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never became true")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- tests ----

func TestAllJobsPassAndPersist(t *testing.T) {
	jobs := []Job{job("j1", 16), job("j2", 16)}
	s, _, _, st := schedFixture(t, schedConfig, jobs)
	waitDone(t, runSched(t, s))
	for _, id := range []string{"j1", "j2"} {
		if st.Jobs[id].Status != StatusPassed {
			t.Errorf("%s = %s, want passed", id, st.Jobs[id].Status)
		}
		if n := len(st.Jobs[id].Attempts); n != 1 {
			t.Errorf("%s attempts = %d, want 1", id, n)
		}
		if st.Jobs[id].Attempts[0].End.IsZero() {
			t.Errorf("%s attempt end not filled", id)
		}
	}
	if got, err := LoadState(s.runDir); err != nil || got.Jobs["j1"].Status != StatusPassed {
		t.Errorf("state not persisted: %v", err)
	}
}

func TestBudgetLimitsConcurrency(t *testing.T) {
	// C3_CPUS 40/region, safety 0.8 -> 32 usable; 16-cpu jobs -> 2 per region, 4 total.
	// container_cap 3 binds first -> maxSeen must be exactly 3 with 6 jobs.
	var jobs []Job
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		jobs = append(jobs, job(id, 16))
	}
	s, ex, _, _ := schedFixture(t, schedConfig, jobs)
	waitDone(t, runSched(t, s))
	if ex.maxSeen > 3 {
		t.Errorf("maxSeen = %d, want <= 3 (container_cap)", ex.maxSeen)
	}
}

func TestNoHeadOfLineBlocking(t *testing.T) {
	// big job (36 cpu > 32 usable/region) can never place; small one behind it must run.
	// Poll via LoadState — reading st directly would race with the scheduler goroutine.
	jobs := []Job{job("big", 36), job("small", 16)}
	s, _, _, _ := schedFixture(t, schedConfig, jobs)
	errc := runSched(t, s)
	deadline := time.After(5 * time.Second)
	for {
		snap, err := LoadState(s.runDir)
		if err == nil && snap.Jobs["small"].Status == StatusPassed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("small never ran behind unplaceable big")
		case <-time.After(10 * time.Millisecond):
		}
	}
	s.stopForTest()
	waitDone(t, errc)
}

func TestMaxParallelPerShape(t *testing.T) {
	var jobs []Job
	for _, id := range []string{"m1", "m2", "m3"} {
		j := job(id, 4)
		j.Shape = "n4a-standard-2"
		j.MaxParallel = 1
		jobs = append(jobs, j)
	}
	s, ex, _, _ := schedFixture(t, schedConfig, jobs)
	// hold every job briefly so overlap would be visible
	for _, id := range []string{"m1", "m2", "m3"} {
		g := make(chan struct{})
		ex.gate[id] = g
		go func() { time.Sleep(50 * time.Millisecond); close(g) }()
	}
	waitDone(t, runSched(t, s))
	if ex.maxSeen > 1 {
		t.Errorf("max_parallel violated: maxSeen = %d", ex.maxSeen)
	}
}

func TestDeadJobCtxStopsRelaunchCycle(t *testing.T) {
	jobs := []Job{job("j1", 16), job("j2", 16)}
	s, ex, _, st := schedFixture(t, schedConfig, jobs)
	// Gate both jobs so both are still in-flight (not yet completed) when
	// jobCtx dies below. A single gate on j1 only left j2 unsynchronized:
	// j2 has no gate, completes in microseconds while jobCtx is still
	// alive, and legitimately lands on StatusPassed — observed failure
	// "j2 status = passed, want queued (resumable)" once the admit()/Run()
	// fix below let the test reach this assertion for the first time.
	// Gating both deterministically holds both jobs in the running set
	// until killJobs fires, which is the state the test claims to exercise.
	g1, g2 := make(chan struct{}), make(chan struct{})
	ex.gate["j1"] = g1
	ex.gate["j2"] = g2
	admitCtx := context.Background() // stays live the whole time
	jobCtx, killJobs := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Run(admitCtx, jobCtx) }()
	waitFor(t, func() bool { return len(ex.callsSnapshot()) >= 2 }) // both in flight
	killJobs() // jobCtx dead, admitCtx alive — the previously-unbounded state
	close(g1)
	close(g2)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("scheduler: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run never returned with dead jobCtx (relaunch cycle)")
	}
	for _, id := range []string{"j1", "j2"} {
		if n := len(st.Jobs[id].Attempts); n > 1 {
			t.Errorf("%s attempts = %d, want <= 1 (no relaunch churn)", id, n)
		}
		if got := st.Jobs[id].Status; got != StatusQueued {
			t.Errorf("%s status = %s, want queued (resumable)", id, got)
		}
	}
}

func TestCooldownBackoffCapped(t *testing.T) {
	s, _, clk, _ := schedFixture(t, schedConfig, []Job{job("j1", 16)})
	for i := 0; i < 40; i++ {
		s.bumpCooldown("c3", "r1")
	}
	until := s.st.Cooldowns["c3|r1"]
	d := until.Sub(clk.Now())
	if d <= 0 {
		t.Fatalf("cooldown in the past after 40 hits (overflow): %v", d)
	}
	if max := 64 * time.Minute; d > max {
		t.Fatalf("cooldown %v exceeds cap %v", d, max)
	}
}

func TestStockoutFailsOverCrossRegion(t *testing.T) {
	jobs := []Job{job("j1", 16)}
	s, ex, _, st := schedFixture(t, schedConfig, jobs)
	ex.script["j1"] = []Result{{Classification: ClassStockout, Detail: "ZONE_RESOURCE_POOL_EXHAUSTED"}, {Classification: ClassPass}}
	waitDone(t, runSched(t, s))
	if st.Jobs["j1"].Status != StatusPassed || len(st.Jobs["j1"].Attempts) != 2 {
		t.Fatalf("want pass after 2 attempts: %+v", st.Jobs["j1"])
	}
	calls := ex.callsSnapshot()
	if zoneRegion(strings.Split(calls[0], "@")[1]) == zoneRegion(strings.Split(calls[1], "@")[1]) {
		t.Errorf("retry stayed in cooled region: %v", calls)
	}
}

func TestCooldownExpiresWithClock(t *testing.T) {
	// single-region job: stockout cools r1; nothing can run until Advance.
	//
	// Adapted from the brief's single waitFor+Advance: the initial persisted
	// state already has j1 at StatusQueued before the scheduler places it
	// for the first time, so waitFor's "back to queued" condition is
	// satisfied at t=0 -- before the first attempt even runs -- and the lone
	// clk.Advance(121*time.Second) races the idleTick waiter the scheduler
	// registers once the retry is actually cooldown-blocked. fakeClock.Advance
	// only wakes waiters registered before it's called, so an Advance that
	// lands early is lost for good and Run() hangs (reproduced 10/10 under
	// -race). Poll with small Advances instead, as TestStockoutAttemptCap
	// does, so whichever Advance call is still running when the waiter
	// registers catches it.
	jobs := []Job{{ID: "j1", BudgetKey: "C3_CPUS", Series: "c3", CPUCost: 16,
		Zones: []string{"r1-a"}, Timeout: Duration(30 * time.Minute)}}
	s, ex, clk, st := schedFixture(t, schedConfig, jobs)
	ex.script["j1"] = []Result{{Classification: ClassStockout}, {Classification: ClassPass}}
	errc := runSched(t, s)
	go func() { // keep expiring the 120s first-hit cooldown so the retry can proceed
		for i := 0; i < 20; i++ {
			time.Sleep(20 * time.Millisecond)
			clk.Advance(20 * time.Minute)
		}
	}()
	waitDone(t, errc)
	if st.Jobs["j1"].Status != StatusPassed {
		t.Fatalf("= %s, want passed", st.Jobs["j1"].Status)
	}
}

func TestStockoutAttemptCap(t *testing.T) {
	jobs := []Job{job("j1", 16)}
	s, ex, clk, st := schedFixture(t, schedConfig, jobs)
	ex.script["j1"] = []Result{{Classification: ClassStockout}, {Classification: ClassStockout}, {Classification: ClassStockout}, {Classification: ClassStockout}}
	errc := runSched(t, s)
	go func() { // keep expiring cooldowns so retries can proceed
		for i := 0; i < 20; i++ {
			time.Sleep(20 * time.Millisecond)
			clk.Advance(20 * time.Minute)
		}
	}()
	waitDone(t, errc)
	if st.Jobs["j1"].Status != StatusFailedInfra || len(st.Jobs["j1"].Attempts) != 4 {
		t.Fatalf("want failed_infra after 4 attempts: %s / %d", st.Jobs["j1"].Status, len(st.Jobs["j1"].Attempts))
	}
}

func TestTimeoutRetriesOnce(t *testing.T) {
	jobs := []Job{job("j1", 16)}
	s, ex, _, st := schedFixture(t, schedConfig, jobs)
	ex.script["j1"] = []Result{{Classification: ClassTimeout}, {Classification: ClassTimeout}}
	waitDone(t, runSched(t, s))
	if st.Jobs["j1"].Status != StatusFailedInfra || len(st.Jobs["j1"].Attempts) != 2 {
		t.Fatalf("want failed_infra after 2 timeout attempts: %+v", st.Jobs["j1"])
	}
}

func TestRealFailureNeverRetries(t *testing.T) {
	jobs := []Job{job("j1", 16)}
	s, ex, _, st := schedFixture(t, schedConfig, jobs)
	ex.script["j1"] = []Result{{Classification: ClassFail}}
	waitDone(t, runSched(t, s))
	if st.Jobs["j1"].Status != StatusFailedReal || len(st.Jobs["j1"].Attempts) != 1 {
		t.Fatalf("want failed_real after 1 attempt: %+v", st.Jobs["j1"])
	}
}

func TestPauseFileBlocksAdmission(t *testing.T) {
	jobs := []Job{job("j1", 16)}
	s, _, clk, st := schedFixture(t, schedConfig, jobs)
	os.WriteFile(filepath.Join(s.runDir, "pause"), nil, 0o644)
	errc := runSched(t, s)
	clk.Advance(idleTick)
	time.Sleep(50 * time.Millisecond)
	snap, err := LoadState(s.runDir) // not st: it races with the scheduler goroutine
	if err != nil {
		t.Fatal(err)
	}
	if snap.Jobs["j1"].Status != StatusQueued {
		t.Fatalf("admitted while paused")
	}
	os.Remove(filepath.Join(s.runDir, "pause"))
	clk.Advance(idleTick)
	waitDone(t, errc)
	if st.Jobs["j1"].Status != StatusPassed {
		t.Fatalf("did not resume after unpause: %s", st.Jobs["j1"].Status)
	}
}

func TestDrainStopsAdmissionAndRequeuesAborted(t *testing.T) {
	jobs := []Job{job("j1", 16), job("j2", 16)}
	s, ex, _, st := schedFixture(t, schedConfig, jobs)
	g1 := make(chan struct{})
	ex.gate["j1"] = g1
	admitCtx, stopAdmit := context.WithCancel(context.Background())
	jobCtx, killJobs := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Run(admitCtx, jobCtx) }()
	waitFor(t, func() bool { return len(ex.callsSnapshot()) >= 1 })
	stopAdmit() // j2 must never launch after this if not already launched
	killJobs()  // simulate second Ctrl-C: abort j1
	close(g1)
	waitDone(t, errc)
	if st.Jobs["j1"].Status != StatusQueued {
		t.Errorf("aborted job should be requeued, got %s", st.Jobs["j1"].Status)
	}
	if got := st.Jobs["j1"].Attempts[len(st.Jobs["j1"].Attempts)-1].Result; got != "aborted" {
		t.Errorf("attempt result = %q, want aborted", got)
	}
}
