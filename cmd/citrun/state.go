// cmd/citrun/state.go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type JobStatus string

const (
	StatusQueued      JobStatus = "queued"
	StatusRunning     JobStatus = "running"
	StatusPassed      JobStatus = "passed"
	StatusFailedReal  JobStatus = "failed_real"
	StatusFailedInfra JobStatus = "failed_infra"
	StatusQuarantined JobStatus = "skipped_quarantine"
)

type Attempt struct {
	Zone        string    `json:"zone"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end,omitzero"`
	WallSeconds float64   `json:"wall_seconds"`
	Result      string    `json:"result"`
	Detail      string    `json:"detail,omitempty"`
}

type JobRecord struct {
	Job      Job       `json:"job"`
	Status   JobStatus `json:"status"`
	Attempts []Attempt `json:"attempts,omitempty"`
}

type RunState struct {
	RunID     string                `json:"run_id"`
	Started   time.Time             `json:"started"`
	Order     []string              `json:"order"`
	Jobs      map[string]*JobRecord `json:"jobs"`
	Cooldowns map[string]time.Time  `json:"cooldowns,omitempty"`
}

func NewRunState(runID string, now time.Time, jobs []Job) *RunState {
	st := &RunState{RunID: runID, Started: now, Jobs: map[string]*JobRecord{}, Cooldowns: map[string]time.Time{}}
	for _, j := range jobs {
		status := StatusQueued
		if j.Quarantined != "" {
			status = StatusQuarantined
		}
		st.Order = append(st.Order, j.ID)
		st.Jobs[j.ID] = &JobRecord{Job: j, Status: status}
	}
	return st
}

func SaveState(runDir string, st *RunState) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(runDir, "state.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(runDir, "state.json"))
}

func LoadState(runDir string) (*RunState, error) {
	raw, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		return nil, err
	}
	var st RunState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	if st.Cooldowns == nil {
		st.Cooldowns = map[string]time.Time{}
	}
	return &st, nil
}

type Event struct {
	TS      time.Time `json:"ts"`
	Job     string    `json:"job"`
	Type    string    `json:"type"`
	Zone    string    `json:"zone,omitempty"`
	Attempt int       `json:"attempt"`
	Detail  string    `json:"detail,omitempty"`
}

func AppendEvent(runDir string, ev Event) error {
	f, err := os.OpenFile(filepath.Join(runDir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	return err
}
