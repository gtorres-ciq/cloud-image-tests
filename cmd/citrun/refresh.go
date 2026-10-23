package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// refreshRunConfig validates all replacements before changing state. Call only
// with the original runner stopped; this is not a concurrent-state editor.
func refreshRunConfig(dir string, st *RunState, cfg *Config) (int, string, error) {
	fresh, err := ExpandJobs(cfg, JobFilter{})
	if err != nil {
		return 0, "", err
	}
	byID := make(map[string]Job, len(fresh))
	for _, j := range fresh {
		if _, exists := byID[j.ID]; exists {
			return 0, "", fmt.Errorf("duplicate expanded job ID %s", j.ID)
		}
		byID[j.ID] = j
	}
	next := *st
	next.Cooldowns = make(map[string]time.Time, len(st.Cooldowns))
	for key, until := range st.Cooldowns {
		next.Cooldowns[key] = until
	}
	next.Jobs = make(map[string]*JobRecord, len(st.Jobs))
	var candidates []Job
	for id, rec := range st.Jobs {
		if rec.Status == StatusRunning {
			return 0, "", fmt.Errorf("job %s is running; stop the runner and reconcile running jobs with ordinary resume before refreshing", id)
		}
		copyRec := *rec
		next.Jobs[id] = &copyRec
		switch rec.Status {
		case StatusQueued, StatusFailedInfra, StatusFailedReal:
			j, ok := byID[id]
			if !ok || rec.Job.ID != id {
				return 0, "", fmt.Errorf("saved job %s cannot be matched in edited config", id)
			}
			copyRec.Job.Zones = append([]string(nil), j.Zones...)
			copyRec.Job.CPUCost = j.CPUCost
			copyRec.Job.BudgetKey = j.BudgetKey
			// New placement or budget rules require a fresh sweep.
			copyRec.StockoutZones = nil
			copyRec.StockoutRounds = 0
			delete(next.Cooldowns, "stockout|"+id)
			// Validate what will actually run, not the expanded job's new
			// quarantine or other fields that this operation does not refresh.
			candidate := copyRec.Job
			candidate.Quarantined = ""
			candidates = append(candidates, candidate)
		}
	}
	if err := validatePlaceable(cfg, candidates); err != nil {
		return 0, "", err
	}
	if len(candidates) == 0 {
		return 0, "", nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return 0, "", err
	}
	backup, err := os.CreateTemp(dir, "state.json.pre-refresh-*")
	if err != nil {
		return 0, "", err
	}
	name := backup.Name()
	_, writeErr := backup.Write(raw)
	closeErr := backup.Close()
	if writeErr != nil {
		return 0, name, writeErr
	}
	if closeErr != nil {
		return 0, name, closeErr
	}
	if err := SaveState(dir, &next); err != nil {
		return 0, name, err
	}
	*st = next
	return len(candidates), name, nil
}
