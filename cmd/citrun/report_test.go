// cmd/citrun/report_test.go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reportFixture(t *testing.T) (*RunState, string) {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	mk := func(id, cfgName string) Job {
		return Job{ID: id, Config: cfgName, BaseImage: "img", Shape: "shp", Suite: strings.Split(id, "_")[0]}
	}
	st := NewRunState("r", now, []Job{mk("ssh_1", "x86"), mk("net_1", "x86"), mk("cvm_1", "arm"), mk("disk_1", "x86")})
	p := st.Jobs["ssh_1"]
	p.Status = StatusPassed
	p.Attempts = []Attempt{
		{Zone: "europe-west1-b", Start: now, End: now.Add(2 * time.Minute), WallSeconds: 120, Result: ClassStockout},
		{Zone: "us-central1-c", Start: now, End: now.Add(7 * time.Minute), WallSeconds: 420, Result: ClassPass},
	}
	f := st.Jobs["net_1"]
	f.Status = StatusFailedReal
	f.Attempts = []Attempt{{Zone: "z", WallSeconds: 60, Result: ClassFail}}
	jd := filepath.Join(dir, "jobs", "net_1")
	os.MkdirAll(jd, 0o755)
	os.WriteFile(filepath.Join(jd, "junit.xml"),
		[]byte(`<testsuites><testsuite><testcase name="TestNIC"><failure message="nic count wrong"/></testcase></testsuite></testsuites>`), 0o644)
	i := st.Jobs["cvm_1"]
	i.Status = StatusFailedInfra
	i.Attempts = []Attempt{{Zone: "z", Result: ClassStockout, Detail: "ZONE_RESOURCE_POOL_EXHAUSTED"}}
	st.Jobs["disk_1"].Status = StatusQuarantined
	st.Jobs["disk_1"].Job.Quarantined = "known bad"
	return st, dir
}

func TestBuildReport(t *testing.T) {
	st, dir := reportFixture(t)
	md, csv := BuildReport(st, dir, 5)
	for _, want := range []string{
		"x86", "passed 1", // per-config summary
		"nic count wrong",              // junit message surfaced for real failure
		"ZONE_RESOURCE_POOL_EXHAUSTED", // infra detail
		"europe-west1: 1",              // stockout by region
		"ssh_1",                        // flake listed (passed after stockout)
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
	if !strings.Contains(csv, "config,image,shape,suite,status,attempts,wall_seconds,zones,reason") {
		t.Errorf("csv header wrong")
	}
	if !strings.Contains(csv, "x86,img,shp,ssh,passed,2,540,europe-west1-b us-central1-c,") {
		t.Errorf("csv row wrong:\n%s", csv)
	}
}

func TestWriteReportFiles(t *testing.T) {
	st, dir := reportFixture(t)
	if err := writeReportFiles(dir, st, 5); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"report.md", "report.csv"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || fi.Size() == 0 {
			t.Errorf("%s missing/empty: %v", f, err)
		}
	}
}
