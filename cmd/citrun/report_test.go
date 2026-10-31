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
	st := NewRunState("r", now, []Job{mk("ssh_1", "x86"), mk("net_1", "x86"), mk("cvm_1", "arm"), mk("disk_1", "x86"), mk("infra_1", "x86")})
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
	// A second failed_real job whose junit has multiple failing testcases and
	// a CDATA-body detail (the shape the manager writes when a VM dies
	// mid-suite: one <failure> per testcase, message attribute empty).
	m := st.Jobs["cvm_1"]
	m.Status = StatusFailedReal
	m.Attempts = []Attempt{{Zone: "z", WallSeconds: 60, Result: ClassFail}}
	mjd := filepath.Join(dir, "jobs", "cvm_1")
	os.MkdirAll(mjd, 0o755)
	os.WriteFile(filepath.Join(mjd, "junit.xml"),
		[]byte(`<testsuites><testsuite>`+
			`<testcase name="TestBootA"><failure message="" type="Failure"><![CDATA[VM died mid-suite]]></failure></testcase>`+
			`<testcase name="TestBootB"><failure message="" type="Failure"><![CDATA[VM died mid-suite]]></failure></testcase>`+
			`<testcase name="TestBootC"><failure message="" type="Failure"><![CDATA[VM died mid-suite]]></failure></testcase>`+
			`<testcase name="TestSkipped"><skipped message="not applicable"/></testcase>`+
			`</testsuite></testsuites>`), 0o644)
	i := st.Jobs["disk_1"]
	i.Status = StatusFailedInfra
	i.Attempts = []Attempt{{Zone: "z", Result: ClassStockout, Detail: "ZONE_RESOURCE_POOL_EXHAUSTED"}}
	// Quarantine overrides failure status on disk_1; the infra job itself is
	// a dedicated record so the Infra Failures section still renders.
	st.Jobs["disk_1"].Status = StatusQuarantined
	st.Jobs["disk_1"].Job.Quarantined = "known bad"
	st.Jobs["infra_1"].Status = StatusFailedInfra
	st.Jobs["infra_1"].Attempts = []Attempt{{Zone: "z", Result: ClassStockout, Detail: "ZONE_RESOURCE_POOL_EXHAUSTED"}}
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
		"shp: 3",                       // stockout by shape (2 infra jobs + ssh flake)
		"shp/europe-west1: 1",          // stockout by shape/region combo
		"ssh_1",                        // flake listed (passed after stockout)
		"VM died mid-suite",            // CDATA detail for multi-failure job
		"TestBootA",                    // failed testcases enumerated
		"(3 failed tests)",             // count matches junit testcase count
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
	// The skipped testcase must not appear in the failed list.
	if strings.Contains(md, "TestSkipped") {
		t.Errorf("skipped testcase leaked into failure list:\n%s", md)
	}
	if !strings.Contains(csv, "config,image,shape,suite,status,attempts,wall_seconds,zones,reason") {
		t.Errorf("csv header wrong")
	}
	if !strings.Contains(csv, "x86,img,shp,ssh,passed,2,540,europe-west1-b us-central1-c,") {
		t.Errorf("csv row wrong:\n%s", csv)
	}
	// failed_real reason prefers junit: "<first failing test>: <detail>".
	if !strings.Contains(csv, "arm,img,shp,cvm,failed_real,1,60,z,TestBootA: VM died mid-suite") {
		t.Errorf("csv failed_real reason wrong:\n%s", csv)
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
