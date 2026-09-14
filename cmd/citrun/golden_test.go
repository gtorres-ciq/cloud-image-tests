// cmd/citrun/golden_test.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// cellsFor renders jobs of one config in the bash harness's format:
// "CELL <full-image> <shape> <suite>", sorted.
func cellsFor(jobs []Job, config string) []string {
	var out []string
	for _, j := range jobs {
		if j.Config == config {
			out = append(out, fmt.Sprintf("CELL %s %s %s", j.Image, j.Shape, j.Suite))
		}
	}
	sort.Strings(out)
	return out
}

func readGolden(t *testing.T, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "tools", "testdata", name))
	if err != nil {
		t.Fatalf("golden %s missing — generate it per the Phase 0 plan Task 1: %v", name, err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	sort.Strings(lines)
	return lines
}

func TestGoldenParity(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "citrun.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := ExpandJobs(cfg, JobFilter{})
	if err != nil {
		t.Fatal(err)
	}

	for cfgName, golden := range map[string]string{
		"x86": "cells-x86.txt", "arm": "cells-arm.txt", "arm-metal": "cells-arm-metal.txt",
	} {
		got := cellsFor(jobs, cfgName)
		want := readGolden(t, golden)
		if len(got) != len(want) {
			t.Errorf("%s: %d cells, golden has %d", cfgName, len(got), len(want))
		}
		for i := range want {
			g := "<missing>"
			if i < len(got) {
				g = got[i]
			}
			if g != want[i] {
				t.Fatalf("%s: first mismatch at line %d:\n  got:  %s\n  want: %s", cfgName, i, g, want[i])
			}
		}
	}

	if n := len(cellsFor(jobs, "x86-metal")); n != 117 {
		t.Errorf("x86-metal cells = %d, want 117 (13 images x 9 suites)", n)
	}
	if n := len(cellsFor(jobs, "u4s")); n != 24 {
		t.Errorf("u4s cells = %d, want 24 (1 image x 12 suites x 2 zones)", n)
	}
	u4sZones := map[string]int{}
	for _, j := range jobs {
		if j.Config != "u4s" {
			continue
		}
		if j.BaseImage != "rocky-linux-10-optimized-gcp-oot-gve" || j.Shape != "u4s-standard-4" {
			t.Errorf("unexpected u4s cell: %+v", j)
		}
		if j.Suite == "livemigrate" {
			t.Errorf("u4s must not run unsupported livemigrate suite: %+v", j)
		}
		if j.MaxParallel != 1 || j.BudgetKey != "U4S_CPUS" {
			t.Errorf("u4s cell %q has unsafe quota controls: MaxParallel=%d BudgetKey=%q", j.ID, j.MaxParallel, j.BudgetKey)
		}
		if len(j.Zones) != 1 {
			t.Errorf("u4s cell %q must target exactly one zone, got %v", j.ID, j.Zones)
			continue
		}
		u4sZones[j.Zones[0]]++
	}
	if u4sZones["us-south1-d"] != 12 || u4sZones["us-south1-e"] != 12 {
		t.Errorf("u4s zone coverage = %v, want 12 jobs in each us-south1 zone", u4sZones)
	}
	if n := len(cellsFor(jobs, "shapevalidation")); n != 10 {
		t.Errorf("shapevalidation jobs = %d, want 10", n)
	}
	if len(jobs) != 2179 {
		t.Errorf("total jobs = %d, want 2179", len(jobs))
	}
}
