// cmd/citrun/scheduler.go
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

const (
	capStockout     = 4 // completed placement sweeps, not individual attempts
	capQuota        = 4
	capTimeout      = 2
	capError        = 2
	cooldownBase    = 120 * time.Second
	quotaPenaltyDur = 10 * time.Minute
	idleTick        = 5 * time.Second
)

type completion struct {
	jobID, zone string
	start, end  time.Time
	res         Result
}

type Scheduler struct {
	cfg    *Config
	st     *RunState
	exec   Executor
	clock  Clock
	runDir string

	running         int
	runningPerShape map[string]int
	usedCPU         map[string]map[string]int // region -> budget key -> in use
	usedNets        int
	usedSubnets     int
	cooldownHits    map[string]int       // "quota|budgetKey|region"
	quotaPenalty    map[string]time.Time // "budgetKey|region" -> until
	zoneRR          map[string]int
	done            chan completion
	jobCtx          context.Context
	stopCh          chan struct{}
	stopOnce        sync.Once
}

func NewScheduler(cfg *Config, st *RunState, ex Executor, clk Clock, runDir string) *Scheduler {
	// Old runs stored both stockouts and quota failures as series|region.
	// Retain recent quota restrictions, but discard the broad stockout timers:
	// the new per-job sweeps must be allowed to try every eligible zone.
	for key, until := range st.Cooldowns {
		if strings.HasPrefix(key, "quota|") || strings.HasPrefix(key, "stockout|") {
			continue
		}
		if clk.Now().Before(until) {
			for _, rec := range st.Jobs {
				for _, a := range rec.Attempts {
					if a.Result == ClassQuota && rec.Job.Series+"|"+zoneRegion(a.Zone) == key && a.End.After(clk.Now().Add(-64*time.Minute)) {
						qkey := "quota|" + rec.Job.BudgetKey + "|" + zoneRegion(a.Zone)
						if until.After(st.Cooldowns[qkey]) {
							st.Cooldowns[qkey] = until
						}
					}
				}
			}
		}
		delete(st.Cooldowns, key)
	}
	return &Scheduler{
		cfg: cfg, st: st, exec: ex, clock: clk, runDir: runDir,
		runningPerShape: map[string]int{}, usedCPU: map[string]map[string]int{},
		cooldownHits: map[string]int{}, quotaPenalty: map[string]time.Time{},
		zoneRR: map[string]int{}, done: make(chan completion), stopCh: make(chan struct{}),
	}
}

// stopForTest cancels admission race-free from another goroutine.
func (s *Scheduler) stopForTest() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

func (s *Scheduler) paused() bool {
	_, err := os.Stat(filepath.Join(s.runDir, "pause"))
	return err == nil
}

func (s *Scheduler) queuedRemaining() int {
	n := 0
	for _, id := range s.st.Order {
		if s.st.Jobs[id].Status == StatusQueued {
			n++
		}
	}
	return n
}

// budget keys a job needs headroom in for a region: CPUS always, plus its
// BudgetKey where the region defines it.
func (s *Scheduler) budgetKeys(job Job, region string) []string {
	keys := []string{"CPUS"}
	if job.BudgetKey != "CPUS" {
		if _, ok := s.cfg.Budgets.Regions[region][job.BudgetKey]; ok {
			keys = append(keys, job.BudgetKey)
		}
	}
	return keys
}

func (s *Scheduler) allowed(region, key string) int {
	limit := s.cfg.Budgets.Regions[region][key]
	a := int(float64(limit) * s.cfg.SafetyFactor)
	if until, ok := s.quotaPenalty[key+"|"+region]; ok && s.clock.Now().Before(until) {
		a /= 2
	}
	return a
}

func (s *Scheduler) headroom(job Job, region string) int {
	h := 1 << 30
	for _, key := range s.budgetKeys(job, region) {
		free := s.allowed(region, key) - s.usedCPU[region][key]
		if free < h {
			h = free
		}
	}
	return h
}

// avoidZone returns the zone of the last stockout/quota attempt for a
// requeued job. Measured 2026-08→10 (runs/): retrying in the same zone after
// a stockout passes ~1% of the time, while moving to a different zone passes
// 82%. Prefer another zone; the last zone is a fallback after a full sweep.
func avoidZone(rec *JobRecord) string {
	if len(rec.Attempts) == 0 {
		return ""
	}
	last := rec.Attempts[len(rec.Attempts)-1]
	switch last.Result {
	case ClassStockout, ClassQuota:
		return last.Zone
	default:
		return ""
	}
}

// fitsConfiguredBudget ignores transient occupancy and quota penalties. Busy
// regions are still part of a sweep; regions that can never fit are not.
func (s *Scheduler) fitsConfiguredBudget(job Job, region string) bool {
	for _, key := range s.budgetKeys(job, region) {
		if int(float64(s.cfg.Budgets.Regions[region][key])*s.cfg.SafetyFactor) < job.CPUCost {
			return false
		}
	}
	return true
}

func (s *Scheduler) pickZone(job Job, avoid string) string {
	now := s.clock.Now()
	rec := s.st.Jobs[job.ID]
	key := "stockout|" + job.ID
	if until, ok := s.st.Cooldowns[key]; ok {
		if now.Before(until) {
			return ""
		}
		delete(s.st.Cooldowns, key)
		if rec != nil {
			rec.StockoutZones = nil
		}
	}
	// Prefer a different zone, but allow the last zone when it is the only
	// feasible placement after an exhausted sweep (including budget constraints).
	for _, skipLast := range []bool{true, false} {
		regions := map[string][]string{}
		var order []string
		for _, z := range job.Zones {
			if skipLast && z == avoid {
				continue
			}
			if rec != nil && rec.StockoutZones[z] {
				continue
			}
			r := zoneRegion(z)
			if until := s.st.Cooldowns["quota|"+job.BudgetKey+"|"+r]; now.Before(until) {
				continue
			}
			if len(regions[r]) == 0 {
				order = append(order, r)
			}
			regions[r] = append(regions[r], z)
		}
		sort.Strings(order)
		best, bestFree := "", -1
		for _, r := range order {
			free := s.headroom(job, r)
			if free >= job.CPUCost && free > bestFree {
				best, bestFree = r, free
			}
		}
		if best != "" {
			zones := regions[best]
			z := zones[s.zoneRR[best]%len(zones)]
			s.zoneRR[best]++
			return z
		}
	}
	return ""
}

// A failure eliminates only this job's failed shape/zone placement. Complete
// sweeps, not simultaneous failures of unrelated jobs, drive the backoff.
func (s *Scheduler) recordStockout(rec *JobRecord, zone string) {
	if rec.StockoutZones == nil {
		rec.StockoutZones = map[string]bool{}
	}
	rec.StockoutZones[zone] = true
	rec.Status = StatusQueued
	for _, z := range rec.Job.Zones {
		if s.fitsConfiguredBudget(rec.Job, zoneRegion(z)) && !rec.StockoutZones[z] {
			return
		}
	}
	rec.StockoutRounds++
	if rec.StockoutRounds >= capStockout {
		rec.Status = StatusFailedInfra
		return
	}
	s.st.Cooldowns["stockout|"+rec.Job.ID] = s.clock.Now().Add(cooldownBase * time.Duration(1<<(rec.StockoutRounds-1)))
}

func (s *Scheduler) charge(job Job, region string, sign int) {
	if s.usedCPU[region] == nil {
		s.usedCPU[region] = map[string]int{}
	}
	for _, key := range s.budgetKeys(job, region) {
		s.usedCPU[region][key] += sign * job.CPUCost
	}
	s.usedNets += sign * job.Networks
	s.usedSubnets += sign * job.Subnets
	s.running += sign
	s.runningPerShape[job.Shape] += sign
}

func (s *Scheduler) persist(jobID, evType, zone string, attempt int, detail string) {
	if err := SaveState(s.runDir, s.st); err != nil {
		log.Printf("WARN: persist state: %v", err)
	}
	if err := AppendEvent(s.runDir, Event{TS: s.clock.Now(), Job: jobID, Type: evType, Zone: zone, Attempt: attempt, Detail: detail}); err != nil {
		log.Printf("WARN: append event: %v", err)
	}
}

func (s *Scheduler) launch(job Job, zone string) {
	rec := s.st.Jobs[job.ID]
	region := zoneRegion(zone)
	s.charge(job, region, +1)
	rec.Status = StatusRunning
	start := s.clock.Now()
	rec.Attempts = append(rec.Attempts, Attempt{Zone: zone, Start: start, Result: "running"})
	s.persist(job.ID, "launched", zone, len(rec.Attempts), "")

	jobDir := filepath.Join(s.runDir, "jobs", job.ID)
	go func() {
		if err := os.MkdirAll(jobDir, 0o755); err != nil {
			s.done <- completion{job.ID, zone, start, s.clock.Now(), Result{ClassError, err.Error()}}
			return
		}
		res := s.exec.Run(s.jobCtx, job, zone, jobDir)
		s.done <- completion{job.ID, zone, start, s.clock.Now(), res}
	}()
}

func (s *Scheduler) admit(admitCtx context.Context) {
	if admitCtx.Err() != nil || s.jobCtx.Err() != nil || s.paused() {
		return
	}
	for _, id := range s.st.Order {
		rec := s.st.Jobs[id]
		if rec.Status != StatusQueued {
			continue
		}
		if s.running >= s.cfg.ContainerCap {
			return
		}
		j := rec.Job
		if j.MaxParallel > 0 && s.runningPerShape[j.Shape] >= j.MaxParallel {
			continue
		}
		if s.usedNets+j.Networks > s.cfg.Budgets.Networks || s.usedSubnets+j.Subnets > s.cfg.Budgets.Subnetworks {
			continue
		}
		zone := s.pickZone(j, avoidZone(rec))
		if zone == "" {
			continue
		}
		s.launch(j, zone)
	}
}

func countClass(attempts []Attempt, class string) int {
	n := 0
	for _, a := range attempts {
		if a.Result == class {
			n++
		}
	}
	return n
}

func (s *Scheduler) handleCompletion(c completion) {
	rec := s.st.Jobs[c.jobID]
	s.charge(rec.Job, zoneRegion(c.zone), -1)
	att := &rec.Attempts[len(rec.Attempts)-1]
	att.End = c.end
	att.WallSeconds = c.end.Sub(c.start).Seconds()
	att.Result = c.res.Classification
	att.Detail = c.res.Detail

	if s.jobCtx.Err() != nil {
		att.Result = "aborted"
		rec.Status = StatusQueued // does not count against any cap
		s.persist(c.jobID, "aborted", c.zone, len(rec.Attempts), c.res.Detail)
		return
	}

	requeueOr := func(class string, cap int) JobStatus {
		if countClass(rec.Attempts, class) >= cap {
			return StatusFailedInfra
		}
		return StatusQueued
	}

	switch c.res.Classification {
	case ClassPass:
		rec.Status = StatusPassed
	case ClassFail:
		rec.Status = StatusFailedReal
	case ClassStockout:
		s.recordStockout(rec, c.zone)
	case ClassQuota:
		s.bumpCooldown("quota|"+rec.Job.BudgetKey, zoneRegion(c.zone))
		s.quotaPenalty[rec.Job.BudgetKey+"|"+zoneRegion(c.zone)] = s.clock.Now().Add(quotaPenaltyDur)
		rec.Status = requeueOr(ClassQuota, capQuota)
	case ClassTimeout:
		rec.Status = requeueOr(ClassTimeout, capTimeout)
	default:
		rec.Status = requeueOr(ClassError, capError)
	}
	s.persist(c.jobID, "completed", c.zone, len(rec.Attempts), c.res.Classification+" "+c.res.Detail)
}

func (s *Scheduler) bumpCooldown(series, region string) {
	key := series + "|" + region
	if s.clock.Now().Before(s.st.Cooldowns[key]) {
		return
	}
	s.cooldownHits[key]++
	hits := s.cooldownHits[key]
	if hits > 6 {
		hits = 6 // cap backoff at 120s * 2^5 = 64m; counter keeps counting, delay doesn't
	}
	d := cooldownBase * (1 << (hits - 1))
	s.st.Cooldowns[key] = s.clock.Now().Add(d)
}

func (s *Scheduler) Run(admitCtx, jobCtx context.Context) error {
	admitCtx, cancel := context.WithCancel(admitCtx)
	defer cancel()
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-admitCtx.Done():
		}
	}()
	s.jobCtx = jobCtx
	admitDone := admitCtx.Done()
	for {
		s.admit(admitCtx)
		if s.running == 0 && (s.queuedRemaining() == 0 || admitCtx.Err() != nil || s.jobCtx.Err() != nil) {
			return SaveState(s.runDir, s.st)
		}
		if s.running == 0 && s.queuedRemaining() > 0 {
			// everything queued is blocked (cooldown/budget); wait for time to pass
			select {
			case <-s.clock.After(idleTick):
			case c := <-s.done:
				s.handleCompletion(c)
			case <-admitDone:
				admitDone = nil
			}
			continue
		}
		select {
		case c := <-s.done:
			s.handleCompletion(c)
		case <-s.clock.After(idleTick):
		case <-admitDone:
			admitDone = nil
		}
	}
}
