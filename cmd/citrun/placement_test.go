// cmd/citrun/placement_test.go
package main

import (
	"strings"
	"testing"
	"time"
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

const excludedZonesConfig = `
project: p
image_prefix: projects/imgs/global/images/family
budgets:
  networks: 10
  subnetworks: 20
  regions:
    r1: {CPUS: 100, C3_CPUS: 50}
    r2: {CPUS: 20}
zone_sets:
  zs1: [r1-a, r2-a]
shapes:
  small: {arch: x86, cpus: 4,  family: CPUS,     zone_set: zs1}
  fam:   {arch: x86, cpus: 30, family: C3_CPUS,  zone_set: zs1}
  huge:  {arch: x86, cpus: 90, family: CPUS,     zone_set: zs1}
suites:
  ssh: {vms: 1, timeout: 30m, est: 9m}
matrix:
  - name: x86
    shapes: [small, fam, huge]
    images: [img-1, img-2]
    suites: [ssh]
`

// TestExcludedZones: small fits everywhere; fam (30 vCPU, C3_CPUS) fits r1
// but r2 lacks the C3_CPUS key so only CPUS=20 usable there — excluded and
// reported as missing; huge (90 vCPU) fits nowhere. Duplicate image jobs of
// the same shape dedupe to one entry per shape×zone.
func TestExcludedZones(t *testing.T) {
	cfg, jobs := loadAndExpand(t, excludedZonesConfig, JobFilter{})
	if err := validatePlaceable(cfg, jobs); err == nil {
		t.Fatal("validatePlaceable: huge fits no region, want error")
	}
	st := NewRunState("r", time.Now(), jobs)
	excl, zeroFit := excludedZones(cfg, st)

	want := map[string]bool{"fam|r2-a": false, "huge|r1-a": false, "huge|r2-a": false}
	for _, e := range excl {
		k := e.Shape + "|" + e.Zone
		found, ok := want[k]
		if !ok {
			t.Errorf("unexpected excluded entry %s (region %s, usable %v)", k, e.Region, e.Usable)
			continue
		}
		if found {
			t.Errorf("duplicate excluded entry %s", k)
		}
		want[k] = true
	}
	for k, found := range want {
		if !found {
			t.Errorf("missing excluded entry %s", k)
		}
	}
	if len(excl) != len(want) {
		t.Errorf("excludedZones length = %d, want %d", len(excl), len(want))
	}
	for _, e := range excl {
		if e.Shape == "fam" && e.Missing != "C3_CPUS" {
			t.Errorf("fam|r2-a Missing = %q, want C3_CPUS", e.Missing)
		}
		if e.Shape == "huge" && e.Missing != "" {
			t.Errorf("huge|r? Missing = %q, want empty", e.Missing)
		}
	}
	if zeroFit != 2 { // img-1_huge_ssh, img-2_huge_ssh
		t.Errorf("zeroFit = %d, want 2", zeroFit)
	}

	quarantined := strings.Replace(excludedZonesConfig, "suites: [ssh]\n",
		"suites: [ssh]\nquarantine:\n  - {shape: huge, reason: \"quarantined\"}\n", 1)
	cfg, jobs = loadAndExpand(t, quarantined, JobFilter{})
	st = NewRunState("r", time.Now(), jobs)
	excl, zeroFit = excludedZones(cfg, st)
	if len(excl) != 1 || excl[0].Shape != "fam" || zeroFit != 0 {
		t.Errorf("quarantined huge must be skipped: excl=%+v zeroFit=%d", excl, zeroFit)
	}
}
