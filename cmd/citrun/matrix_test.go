// cmd/citrun/matrix_test.go
package main

import (
	"strings"
	"testing"
	"time"
)

const rulesConfig = `
project: p
image_prefix: projects/imgs/global/images/family
budgets:
  networks: 10
  subnetworks: 20
  regions:
    r1: {CPUS: 1000, C3_CPUS: 500}
    r2: {CPUS: 1000}
zone_sets:
  main: [r1-a, r1-b, r2-a]
  alt:  [r2-a]
shapes:
  c3-standard-4:  {arch: x86, cpus: 4, family: C3_CPUS, zone_set: main}
  n1-standard-4:  {arch: x86, cpus: 4, family: CPUS, zone_set: main}
  c4-highcpu-4:   {arch: x86, cpus: 4, family: CPUS, zone_set: main}
suites:
  ssh:  {vms: 4, timeout: 30m, est: 9m}
  lssd: {vms: 1, timeout: 15m, est: 3m}
  disk: {vms: 2, timeout: 20m, est: 5m}
  shapevalidation: {vms: 1, timeout: 30m, est: 6m}
matrix:
  - name: x86
    shapes: [c3-standard-4, n1-standard-4, c4-highcpu-4]
    images: [img-a, img-b]
    suites: [ssh, lssd, disk]
rules:
  - suite_only_on: {suites: [lssd], shapes: ["c3-standard*"]}
  - keep_only: {shapes: ["*-highcpu-*"], suites: [ssh]}
  - drop: {images: [img-b], suites: [disk]}
  - zone_set: {shapes: ["c3-standard*"], suites: [ssh], set: alt}
  - timeout: {shapes: ["c3-*"], value: 45m}
quarantine:
  - {image: img-a, suite: disk, reason: "known bad"}
`

func expand(t *testing.T, cfgYAML string, f JobFilter) []Job {
	t.Helper()
	_, jobs := loadAndExpand(t, cfgYAML, f)
	return jobs
}

func find(jobs []Job, id string) *Job {
	for i := range jobs {
		if jobs[i].ID == id {
			return &jobs[i]
		}
	}
	return nil
}

func TestExpandRules(t *testing.T) {
	jobs := expand(t, rulesConfig, JobFilter{})
	ids := map[string]bool{}
	for _, j := range jobs {
		ids[j.ID] = true
	}
	// suite_only_on: lssd exists only on c3-standard-4
	if !ids["img-a_c3-standard-4_lssd"] || ids["img-a_n1-standard-4_lssd"] {
		t.Errorf("suite_only_on wrong: %v", ids)
	}
	// keep_only: highcpu has ssh but not disk
	if !ids["img-a_c4-highcpu-4_ssh"] || ids["img-a_c4-highcpu-4_disk"] {
		t.Errorf("keep_only wrong")
	}
	// drop: img-b disk gone everywhere, img-a disk still present on non-highcpu
	if ids["img-b_c3-standard-4_disk"] || !ids["img-a_c3-standard-4_disk"] {
		t.Errorf("drop wrong")
	}
	j := find(jobs, "img-a_c3-standard-4_ssh")
	if j == nil || len(j.Zones) != 1 || j.Zones[0] != "r2-a" {
		t.Errorf("zone_set override wrong: %+v", j)
	}
	if time.Duration(j.Timeout) != 45*time.Minute {
		t.Errorf("timeout override wrong: %v", j.Timeout)
	}
	if q := find(jobs, "img-a_c3-standard-4_disk"); q == nil || q.Quarantined != "known bad" {
		t.Errorf("quarantine not applied: %+v", q)
	}
	if j.Image != "projects/imgs/global/images/family/img-a" || j.BaseImage != "img-a" {
		t.Errorf("image prefixing wrong: %+v", j)
	}
	if j.CPUCost != 16 || j.BudgetKey != "C3_CPUS" || j.Series != "c3" {
		t.Errorf("cost/budget/series wrong: %+v", j)
	}
}

func TestExpandDeterministic(t *testing.T) {
	a := expand(t, rulesConfig, JobFilter{})
	b := expand(t, rulesConfig, JobFilter{})
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatalf("order not deterministic at %d: %s vs %s", i, a[i].ID, b[i].ID)
		}
	}
}

func TestExpandFilter(t *testing.T) {
	jobs := expand(t, rulesConfig, JobFilter{Suites: []string{"ssh"}, Shapes: []string{"c3-*"}})
	if len(jobs) != 2 { // img-a, img-b on c3-standard-4 only
		t.Fatalf("filter: got %d jobs, want 2: %+v", len(jobs), jobs)
	}
}

func TestExpandEachZoneCreatesIndependentJobs(t *testing.T) {
	cfgYAML := strings.Replace(rulesConfig,
		"  - name: x86\n    shapes:",
		"  - name: x86\n    each_zone: true\n    shapes:", 1)
	jobs := expand(t, cfgYAML, JobFilter{Images: []string{"img-a"}, Shapes: []string{"n1-standard-4"}, Suites: []string{"ssh"}})

	if len(jobs) != 3 {
		t.Fatalf("each-zone jobs = %d, want 3: %+v", len(jobs), jobs)
	}
	want := map[string]string{
		"img-a_n1-standard-4_ssh_r1-a": "r1-a",
		"img-a_n1-standard-4_ssh_r1-b": "r1-b",
		"img-a_n1-standard-4_ssh_r2-a": "r2-a",
	}
	for _, job := range jobs {
		zone, ok := want[job.ID]
		if !ok {
			t.Errorf("unexpected each-zone job ID %q", job.ID)
			continue
		}
		if len(job.Zones) != 1 || job.Zones[0] != zone {
			t.Errorf("job %q zones = %v, want [%s]", job.ID, job.Zones, zone)
		}
		delete(want, job.ID)
	}
	if len(want) != 0 {
		t.Errorf("missing each-zone jobs: %v", want)
	}
}

func TestExpandShapeValidation(t *testing.T) {
	svYAML := strings.Replace(rulesConfig, "quarantine:", `shapevalidation:
  images: [img-a]
  arm_images: [img-arm]
  families:
    - {name: C3, shape: c3-highmem-176, cpus: 176, family: C3_CPUS, arch: x86, zone_set: main}
    - {name: T2A, shape: t2a-standard-48, cpus: 48, family: CPUS, arch: arm64, pinned_zone: r2-a}
quarantine:`, 1)
	jobs := expand(t, svYAML, JobFilter{Configs: []string{"shapevalidation"}})
	if len(jobs) != 2 {
		t.Fatalf("sv jobs = %d, want 2", len(jobs))
	}
	c3 := find(jobs, "img-a_c3-highmem-176_shapevalidation")
	if c3 == nil || c3.Family != "C3" || c3.CPUCost != 176 || c3.Series != "c3" {
		t.Fatalf("sv c3 wrong: %+v", c3)
	}
	t2a := find(jobs, "img-arm_t2a-standard-48_shapevalidation")
	if t2a == nil || len(t2a.Zones) != 1 || t2a.Zones[0] != "r2-a" {
		t.Fatalf("sv pinned wrong: %+v", t2a)
	}
}

const tierConfig = `
project: p
image_prefix: projects/imgs/global/images/family
budgets:
  networks: 10
  subnetworks: 20
  regions:
    r1: {CPUS: 1000, C3_CPUS: 500}
    r2: {CPUS: 1000}
zone_sets:
  main: [r1-a, r1-b]
shapes:
  c3-standard-4:  {arch: x86, cpus: 4, family: C3_CPUS, zone_set: main}
  n1-standard-4:  {arch: x86, cpus: 4, family: CPUS, zone_set: main}
suites:
  ssh:  {vms: 4, timeout: 30m, est: 9m}
  lssd: {vms: 1, timeout: 15m, est: 3m}
matrix:
  - name: x86
    shapes: [c3-standard-4, n1-standard-4]
    images: [img-a, img-b]
    suites: [ssh, lssd]
tiers:
  "1":
    - {shape: c3-standard-4, zones: [r1-c]}
  "2":
    - {shape: n1-standard-4, zones: [r1-c, r1-d]}
`

func TestExpandTierFiltersShapesAndPinsZones(t *testing.T) {
	jobs := expand(t, tierConfig, JobFilter{Tier: "1"})
	// Only the tier's shape expands; the other shape's jobs are gone.
	for _, j := range jobs {
		if j.Shape != "c3-standard-4" {
			t.Errorf("shape %s ran under tier 1 (not in tier): %+v", j.Shape, j)
		}
		if len(j.Zones) != 1 || j.Zones[0] != "r1-c" {
			t.Errorf("job %s zones = %v, want [r1-c]", j.ID, j.Zones)
		}
	}
	if len(jobs) != 4 { // 2 images × 2 suites on the one tier shape
		t.Fatalf("tier 1 jobs = %d, want 4: %+v", len(jobs), jobs)
	}
}

func TestExpandTierPinsZonesOutsideShapeZoneSet(t *testing.T) {
	// r1-c/r1-d are valid budget zones in no zone_set — tier replaces the sweep.
	jobs := expand(t, tierConfig, JobFilter{Tier: "2"})
	for _, j := range jobs {
		if j.Shape != "n1-standard-4" {
			t.Errorf("shape %s ran under tier 2 (not in tier): %+v", j.Shape, j)
		}
		if len(j.Zones) != 2 || j.Zones[0] != "r1-c" || j.Zones[1] != "r1-d" {
			t.Errorf("job %s zones = %v, want [r1-c r1-d]", j.ID, j.Zones)
		}
	}
}

func TestExpandTierAllEqualsFullMatrix(t *testing.T) {
	full := expand(t, tierConfig, JobFilter{})
	all := expand(t, tierConfig, JobFilter{Tier: "all"})
	if len(full) != len(all) {
		t.Fatalf("tier all = %d jobs, full matrix = %d", len(all), len(full))
	}
	for i := range full {
		if full[i].ID != all[i].ID || len(full[i].Zones) != len(all[i].Zones) {
			t.Fatalf("tier all diverges at %d: %s vs %s", i, full[i].ID, all[i].ID)
		}
	}
}

func TestExpandTierExcludesShapeValidation(t *testing.T) {
	svYAML := strings.Replace(tierConfig, "tiers:", `shapevalidation:
  images: [img-a]
  arm_images: [img-arm]
  families:
    - {name: C3, shape: c3-highmem-176, cpus: 176, family: C3_CPUS, arch: x86, zone_set: main}
tiers:`, 1)
	svYAML = strings.Replace(svYAML, "  ssh:  {vms: 4, timeout: 30m, est: 9m}",
		"  ssh:  {vms: 4, timeout: 30m, est: 9m}\n  shapevalidation: {vms: 1, timeout: 30m, est: 6m}", 1)
	full := expand(t, svYAML, JobFilter{Configs: []string{"shapevalidation"}})
	if len(full) != 1 {
		t.Fatalf("full matrix sv jobs = %d, want 1", len(full))
	}
	tiered := expand(t, svYAML, JobFilter{Tier: "1"})
	for _, j := range tiered {
		if j.Suite == "shapevalidation" {
			t.Errorf("shapevalidation ran under tier 1: %+v", j)
		}
	}
}
