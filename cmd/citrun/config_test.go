// cmd/citrun/config_test.go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "citrun.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const miniConfig = `
project: test-proj
image_prefix: projects/imgs/global/images/family
budgets:
  networks: 10
  subnetworks: 20
  regions:
    r1: {CPUS: 100, C3_CPUS: 50}
zone_sets:
  zs1: [r1-a, r1-b]
shapes:
  c3-standard-4: {arch: x86, cpus: 4, family: C3_CPUS, zone_set: zs1}
suites:
  ssh: {vms: 4, timeout: 30m, est: 9m}
matrix:
  - name: x86
    shapes: [c3-standard-4]
    images: [img-1]
    suites: [ssh]
`

func TestLoadConfigDefaultsAndParsing(t *testing.T) {
	cfg, err := LoadConfig(writeTemp(t, miniConfig))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DockerImage != "cloud-image-tests" || cfg.ContainerCap != 80 || cfg.SafetyFactor != 0.8 {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if got := time.Duration(cfg.Suites["ssh"].Timeout); got != 30*time.Minute {
		t.Errorf("timeout = %v, want 30m", got)
	}
}

func TestZoneRegion(t *testing.T) {
	if zoneRegion("europe-west1-b") != "europe-west1" {
		t.Errorf("zoneRegion wrong")
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct{ name, find, replace, wantErr string }{
		{"unknown shape in matrix", "shapes: [c3-standard-4]\n    images", "shapes: [nope]\n    images", "unknown shape"},
		{"unknown suite in matrix", "suites: [ssh]", "suites: [nope]", "unknown suite"},
		{"zone without budget region", "zs1: [r1-a, r1-b]", "zs1: [r9-a]", "no budgets"},
		{"unknown zone_set on shape", "zone_set: zs1", "zone_set: zs9", "unknown zone_set"},
		{"unknown yaml key", "project: test-proj", "project: test-proj\nbogus_key: 1", "field bogus_key not found"},
		{"safety_factor zero rejected", "project: test-proj", "project: test-proj\nsafety_factor: 0", "safety_factor"},
		{"safety_factor over one rejected", "project: test-proj", "project: test-proj\nsafety_factor: 1.5", "safety_factor"},
		{"rule with two actions", "suites: [ssh]", "suites: [ssh]\nrules:\n  - drop: {suites: [ssh]}\n    timeout: {shapes: [c3-standard-4], value: 5m}", "exactly one action"},
		{"rule with zero actions", "suites: [ssh]", "suites: [ssh]\nrules:\n  - {}", "exactly one action"},
		{"shapevalidation family missing pinned_zone and zone_set", "suites: [ssh]", "suites: [ssh]\nshapevalidation:\n  families:\n    - name: fam1", "pinned_zone or zone_set"},
		{"garbage duration value", "timeout: 30m", "timeout: banana", "invalid duration"},
		{"suite_only_on empty suites rejected", "suites: [ssh]", "suites: [ssh]\nrules:\n  - suite_only_on: {shapes: [c3-standard-4]}", "suite_only_on requires non-empty suites"},
		{"keep_only empty shapes rejected", "suites: [ssh]", "suites: [ssh]\nrules:\n  - keep_only: {suites: [ssh]}", "keep_only requires non-empty shapes"},
		{"drop with all fields empty rejected", "suites: [ssh]", "suites: [ssh]\nrules:\n  - drop: {}", "drop requires non-empty"},
		{"zone_set rule empty shapes rejected", "suites: [ssh]", "suites: [ssh]\nrules:\n  - zone_set: {set: zs1}", "zone_set requires non-empty shapes"},
		{"timeout rule empty shapes rejected", "suites: [ssh]", "suites: [ssh]\nrules:\n  - timeout: {value: 5m}", "timeout requires non-empty shapes"},
		{"quarantine entry with no image/shape/suite rejected", "suites: [ssh]", "suites: [ssh]\nquarantine:\n  - {reason: bogus}", "needs image, shape, or suite"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadConfig(writeTemp(t, strings.Replace(miniConfig, c.find, c.replace, 1)))
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, c.wantErr)
			}
		})
	}
}

func TestValidateRejectsEachZoneMatrixWithNoZones(t *testing.T) {
	cfgYAML := strings.Replace(miniConfig, "zs1: [r1-a, r1-b]", "zs1: []", 1)
	cfgYAML = strings.Replace(cfgYAML, "  - name: x86\n    shapes:", "  - name: x86\n    each_zone: true\n    shapes:", 1)

	_, err := LoadConfig(writeTemp(t, cfgYAML))
	if err == nil || !strings.Contains(err.Error(), "each_zone") {
		t.Fatalf("LoadConfig error = %v, want each_zone matrix with no zones rejected", err)
	}
}
