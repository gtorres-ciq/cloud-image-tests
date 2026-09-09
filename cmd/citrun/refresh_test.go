package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRefreshConfigPreservesResults(t *testing.T) {
	cfg, jobs := loadAndExpand(t, rulesConfig, JobFilter{})
	st := NewRunState("test", time.Now(), jobs)
	statuses := []JobStatus{StatusQueued, StatusFailedInfra, StatusFailedReal, StatusPassed, StatusQuarantined}
	for i, id := range st.Order {
		r := st.Jobs[id]
		r.Status = statuses[i%len(statuses)]
		r.Job.CPUCost = 1
		r.Job.BudgetKey = "old"
		r.Job.Zones = []string{"old-a"}
		r.Attempts = []Attempt{{Zone: "old-a", Result: ClassStockout, Detail: "history"}}
	}
	dir := t.TempDir()
	if err := SaveState(dir, st); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	n, backup, err := refreshRunConfig(dir, st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no jobs refreshed")
	}
	b, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(b, before) {
		t.Fatal("backup did not preserve original state")
	}
	for i, id := range st.Order {
		r := st.Jobs[id]
		if r.Status != statuses[i%len(statuses)] || len(r.Attempts) != 1 || r.Attempts[0].Detail != "history" {
			t.Fatalf("lost history/status: %s", id)
		}
		if r.Status == StatusPassed || r.Status == StatusQuarantined {
			if r.Job.CPUCost != 1 || r.Job.BudgetKey != "old" || r.Job.Zones[0] != "old-a" {
				t.Fatalf("modified completed job: %s", id)
			}
		} else {
			want := find(jobs, id)
			if r.Job.CPUCost != want.CPUCost || r.Job.BudgetKey != want.BudgetKey || !reflect.DeepEqual(r.Job.Zones, want.Zones) {
				t.Fatalf("stale job: %s", id)
			}
		}
	}
	saved, err := LoadState(dir)
	wantJSON, _ := json.Marshal(st)
	gotJSON, _ := json.Marshal(saved)
	if err != nil || !bytes.Equal(gotJSON, wantJSON) {
		t.Fatal("refreshed state not persisted")
	}
}

func TestRefreshConfigRejectsWithoutWriting(t *testing.T) {
	for _, problem := range []string{"missing", "running", "budget"} {
		t.Run(problem, func(t *testing.T) {
			cfg, jobs := loadAndExpand(t, rulesConfig, JobFilter{})
			st := NewRunState("test", time.Now(), jobs)
			switch problem {
			case "missing":
				st.Jobs[st.Order[0]].Job.ID = "removed"
			case "running":
				st.Jobs[st.Order[0]].Status = StatusRunning
			case "budget":
				cfg.Budgets.Regions["r1"]["CPUS"] = 0
				cfg.Budgets.Regions["r2"]["CPUS"] = 0
			}
			dir := t.TempDir()
			if err := SaveState(dir, st); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "state.json"))
			if _, _, err := refreshRunConfig(dir, st, cfg); err == nil {
				t.Fatal("expected rejection")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "state.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("modified state on rejection")
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 1 {
				t.Fatal("created backup before validation")
			}
		})
	}
}
