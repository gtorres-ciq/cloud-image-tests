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

	if n := len(cellsFor(jobs, "x86-metal")); n != 108 {
		t.Errorf("x86-metal cells = %d, want 108 (12 images x 9 suites)", n)
	}
	if n := len(cellsFor(jobs, "shapevalidation")); n != 10 {
		t.Errorf("shapevalidation jobs = %d, want 10", n)
	}
	quarantined := 0
	for _, j := range jobs {
		if j.Quarantined != "" {
			quarantined++
		}
	}
	if quarantined != 33 {
		t.Errorf("quarantined = %d, want 33 (3 rocky-8-optimized images x 11 x86 shapes x imageboot)", quarantined)
	}
	if len(jobs) != 2030 {
		t.Errorf("total jobs = %d, want 2030", len(jobs))
	}
}
