package main

import (
	"testing"
)

// TestRepoQuotaFits checks that every expanded job's CPUCost fits each of its
// zones' usable budgets (safety factor applied) — i.e. every job is placeable
// under the REAL per-region family quotas measured 2026-10-14.
func TestRepoQuotaFits(t *testing.T) {
	cfg, err := LoadConfig("../../citrun.yaml")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := ExpandJobs(cfg, JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	sf := cfg.SafetyFactor
	// A job is placeable if at least one of its regions has usable headroom in
	// every budget key (mirrors pickZone's headroom check). Jobs placeable
	// nowhere would sit queued forever.
	fits := func(j Job, r string) bool {
		for _, key := range []string{"CPUS", j.BudgetKey} {
			limit, ok := cfg.Budgets.Regions[r][key]
			if !ok {
				return false
			}
			if j.CPUCost > int(float64(limit)*sf) {
				return false
			}
		}
		return true
	}
	for _, j := range jobs {
		seen := map[string]bool{}
		anyRegion := false
		missingKey := ""
		for _, z := range j.Zones {
			r := zoneRegion(z)
			if seen[r] {
				continue
			}
			seen[r] = true
			if _, ok := cfg.Budgets.Regions[r][j.BudgetKey]; !ok && j.BudgetKey != "CPUS" && missingKey == "" {
				missingKey = r
			}
			if fits(j, r) {
				anyRegion = true
			}
		}
		if missingKey != "" {
			t.Errorf("%s: budget key %s missing for region %s", j.ID, j.BudgetKey, missingKey)
		}
		if !anyRegion {
			t.Errorf("%s: CPUCost %d fits no eligible region — job would queue forever", j.ID, j.CPUCost)
		}
	}
}

// TestRepoMetalC4aSplit checks the arm-metal split: multi-VM suites target
// europe-west4 (the only region with C4A quota ≥ 960 after the 2026-10-14
// increase), 1-VM suites target europe-west3/us-east4/us-east5. No arm-metal
// job may touch us-central1.
func TestRepoMetalC4aSplit(t *testing.T) {
	cfg, err := LoadConfig("../../citrun.yaml")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := ExpandJobs(cfg, JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	big := map[string]string{"loadbalancer": "", "metadata": "", "packagevalidation": ""}
	flex := map[string]string{"suspendresume": "", "licensevalidation": ""}
	nbig, nflex := 0, 0
	for _, j := range jobs {
		if j.Shape != "c4a-highmem-96-metal" {
			continue
		}
		suite := j.Suite
		for _, z := range j.Zones {
			if zoneRegion(z) == "us-central1" {
				t.Errorf("%s targets us-central1; C4A quota in europe-west4 now fits all arm-metal suites", j.ID)
			}
		}
		zr := zoneRegion(j.Zones[0])
		if _, ok := big[suite]; ok {
			nbig++
			if zr != "europe-west4" {
				t.Errorf("%s (multi-VM suite) targets %s, want europe-west4 (C4A=960)", j.ID, zr)
			}
		} else if _, ok := flex[suite]; ok {
			nflex++
			if zr == "us-central1" {
				t.Errorf("%s (1-VM suite) targets us-central1; want metal-c4a regions", j.ID)
			}
		} else {
			t.Errorf("unexpected arm-metal suite %s in %s", suite, j.ID)
		}
	}
	if nbig != 24 { // 8 images x 3 multi-VM suites
		t.Errorf("metal-c4a-big jobs = %d, want 24", nbig)
	}
	if nflex != 16 { // 8 images x 2 one-VM suites
		t.Errorf("metal-c4a-flex jobs = %d, want 16", nflex)
	}
}
