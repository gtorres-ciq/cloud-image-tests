// cmd/citrun/invariants_test.go
package main

import (
	"path/filepath"
	"testing"
)

// TestMatrixInvariants exercises the real production citrun.yaml (unlike
// matrix_test.go / placement_test.go, which use synthetic configs to test the
// expansion engine). Every assertion here encodes a safety property of the
// live matrix that would otherwise be caught only by quota exhaustion or
// failed runs in the GCP project. Deliberate matrix changes should update
// these expectations; an unexpected failure means the matrix changed by
// accident.
func TestMatrixInvariants(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "citrun.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := ExpandJobs(cfg, JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Budgets.Regions["us-south1"]["U4S_CPUS"]; got != 1152 {
		t.Errorf("us-south1 U4S_CPUS budget = %d, want 1152", got)
	}
	if got := cfg.Budgets.Regions["us-south1"]["CPUS"]; got != 1152 {
		t.Errorf("us-south1 CPUS budget = %d, want 1152 so it does not constrain U4S_CPUS", got)
	}
	if got := cfg.Budgets.Regions["us-south1"]["U4C_CPUS"]; got != 480 {
		t.Errorf("us-south1 U4C_CPUS budget = %d, want 480", got)
	}

	if n := countConfig(jobs, "x86-metal"); n != 117 {
		t.Errorf("x86-metal cells = %d, want 117 (13 images x 9 suites)", n)
	}
	if n := countConfig(jobs, "u4s"); n != 24 {
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
	if n := countConfig(jobs, "u4c"); n != 2 {
		t.Errorf("u4c cells = %d, want 2 (1 image x 1 suite x 2 zones)", n)
	}
	u4cZones := map[string]int{}
	for _, j := range jobs {
		if j.Config != "u4c" {
			continue
		}
		if j.BaseImage != "rocky-linux-10-optimized-gcp-oot-gve" || j.Shape != "u4c-highcpu-120-lssd-metal" || j.Suite != "u4c" {
			t.Errorf("unexpected u4c cell: %+v", j)
		}
		if j.MaxParallel != 1 || j.BudgetKey != "U4C_CPUS" || j.CPUCost != 120 {
			t.Errorf("u4c cell %q has unsafe quota controls: MaxParallel=%d BudgetKey=%q CPUCost=%d", j.ID, j.MaxParallel, j.BudgetKey, j.CPUCost)
		}
		if j.Networks != 2 || j.Subnets != 3 {
			t.Errorf("u4c cell %q resource costs = %d networks/%d subnets, want 2/3", j.ID, j.Networks, j.Subnets)
		}
		if len(j.Zones) != 1 {
			t.Errorf("u4c cell %q must target exactly one zone, got %v", j.ID, j.Zones)
			continue
		}
		u4cZones[j.Zones[0]]++
	}
	if u4cZones["us-south1-d"] != 1 || u4cZones["us-south1-e"] != 1 {
		t.Errorf("u4c zone coverage = %v, want one job in each us-south1 zone", u4cZones)
	}
	if n := countConfig(jobs, "shapevalidation"); n != 10 {
		t.Errorf("shapevalidation jobs = %d, want 10", n)
	}
	z4dCount := 0
	for _, j := range jobs {
		if j.Shape != "z4d-highmem-16-standardlssd" {
			continue
		}
		z4dCount++
		if j.Suite == "livemigrate" {
			t.Errorf("z4d must not run unsupported livemigrate suite: %+v", j)
		}
		if j.BudgetKey != "Z4D_CPUS" || j.CPUCost != 16*cfg.Suites[j.Suite].VMs || j.Arch != "x86" {
			t.Errorf("z4d cell %q has wrong shape controls: BudgetKey=%q CPUCost=%d Arch=%q", j.ID, j.BudgetKey, j.CPUCost, j.Arch)
		}
		for _, z := range j.Zones {
			if z != "us-east5-b" && z != "us-east5-c" {
				t.Errorf("z4d cell %q targets zone %s outside the us-east5 quota constraint", j.ID, z)
			}
		}
	}
	if z4dCount != 156 {
		t.Errorf("z4d cells = %d, want 156 (13 images x 12 suites)", z4dCount)
	}
	if got := cfg.Budgets.Regions["us-east5"]["Z4D_CPUS"]; got != 384 {
		t.Errorf("us-east5 Z4D_CPUS budget = %d, want 384", got)
	}
	if len(jobs) != 2337 {
		t.Errorf("total jobs = %d, want 2337", len(jobs))
	}
}

func countConfig(jobs []Job, config string) int {
	n := 0
	for _, j := range jobs {
		if j.Config == config {
			n++
		}
	}
	return n
}
