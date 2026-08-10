// cmd/citrun/state_test.go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	jobs := []Job{
		{ID: "a_b_ssh", Suite: "ssh"},
		{ID: "a_b_disk", Suite: "disk", Quarantined: "known bad"},
	}
	st := NewRunState("r1", now, jobs)
	if st.Jobs["a_b_ssh"].Status != StatusQueued || st.Jobs["a_b_disk"].Status != StatusQuarantined {
		t.Fatalf("initial statuses wrong: %+v", st.Jobs)
	}
	if len(st.Order) != 2 || st.Order[0] != "a_b_ssh" {
		t.Fatalf("order wrong: %v", st.Order)
	}
	st.Jobs["a_b_ssh"].Status = StatusPassed
	st.Jobs["a_b_ssh"].Attempts = []Attempt{{Zone: "z1", Start: now, End: now.Add(5 * time.Minute), WallSeconds: 300, Result: "pass"}}
	if err := SaveState(dir, st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json.tmp")); !os.IsNotExist(err) {
		t.Errorf("tmp file left behind")
	}
	got, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != "r1" || got.Jobs["a_b_ssh"].Status != StatusPassed || got.Jobs["a_b_ssh"].Attempts[0].WallSeconds != 300 {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestAppendEvent(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := AppendEvent(dir, Event{Job: "j", Type: "launched", Attempt: i}); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("events = %d lines, want 3", len(lines))
	}
	var ev Event
	if err := json.Unmarshal([]byte(lines[2]), &ev); err != nil || ev.Attempt != 2 {
		t.Fatalf("bad event line: %v %+v", err, ev)
	}
}
