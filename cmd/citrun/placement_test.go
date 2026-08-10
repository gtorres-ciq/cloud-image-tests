// cmd/citrun/placement_test.go
package main

import (
	"strings"
	"testing"
)

// loadAndExpand loads cfgYAML and expands it, returning both the config and
// the jobs — validatePlaceable needs the config's budgets alongside the jobs.
func loadAndExpand(t *testing.T, cfgYAML string, f JobFilter) (*Config, []Job) {
	t.Helper()
	cfg, err := LoadConfig(writeTemp(t, cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := ExpandJobs(cfg, f)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, jobs
}

func TestValidatePlaceablePasses(t *testing.T) {
	cfg, jobs := loadAndExpand(t, rulesConfig, JobFilter{})
	if err := validatePlaceable(cfg, jobs); err != nil {
		t.Fatalf("validatePlaceable: want nil for fixture that fits its budgets, got %v", err)
	}
}

const placementFailConfig = `
project: p
image_prefix: projects/imgs/global/images/family
budgets:
  networks: 10
  subnetworks: 20
  regions:
    r1: {CPUS: 100, C3_CPUS: 50}
zone_sets:
  zs1: [r1-a, r1-b]
shapes:
  small:  {arch: x86, cpus: 4,   family: CPUS, zone_set: zs1}
  toobig: {arch: x86, cpus: 999, family: CPUS, zone_set: zs1}
suites:
  ssh: {vms: 1, timeout: 30m, est: 9m}
matrix:
  - name: x86
    shapes: [small, toobig]
    images: [img-1]
    suites: [ssh]
`

func TestValidatePlaceableFails(t *testing.T) {
	cfg, jobs := loadAndExpand(t, placementFailConfig, JobFilter{})
	err := validatePlaceable(cfg, jobs)
	if err == nil {
		t.Fatal("validatePlaceable: want error for a job whose CPUCost exceeds every eligible region's usable budget, got nil")
	}
	if !strings.Contains(err.Error(), "img-1_toobig_ssh") {
		t.Errorf("error %q does not name the unplaceable job", err.Error())
	}
}

func TestValidatePlaceableSkipsQuarantined(t *testing.T) {
	cfgYAML := strings.Replace(placementFailConfig, "suites: [ssh]\n",
		"suites: [ssh]\nquarantine:\n  - {shape: toobig, reason: \"never fits, quarantined instead\"}\n", 1)
	cfg, jobs := loadAndExpand(t, cfgYAML, JobFilter{})
	if err := validatePlaceable(cfg, jobs); err != nil {
		t.Fatalf("validatePlaceable: quarantined never-placeable job must not error, got %v", err)
	}
}
