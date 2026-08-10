// cmd/citrun/executor_test.go
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildDockerArgs(t *testing.T) {
	d := &DockerExecutor{Project: "p", RunID: "r1", CredsDir: "/home/u/.config/gcloud", Image: "cloud-image-tests"}
	job := Job{ID: "img_c3-standard-4_ssh", Image: "projects/x/global/images/family/img",
		Shape: "c3-standard-4", Suite: "ssh", Arch: "x86", Timeout: Duration(30 * time.Minute)} // 30m
	got := strings.Join(buildDockerArgs(d, job, "europe-west1-b", "/runs/r1/jobs/j1"), " ")
	for _, want := range []string{
		"--name citrun-r1-img_c3-standard-4_ssh",
		"-v /runs/r1/jobs/j1:/curpath:z",
		"-v /home/u/.config/gcloud:/creds:z",
		"--filter ^(ssh)$",
		"--zones europe-west1-b",
		"--out_path /curpath/junit.xml",
		"--timeout 30m0s",
		"-x86_shape=c3-standard-4",
		"--parallel_count 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q in: %s", want, got)
		}
	}

	arm := job
	arm.Arch = "arm64"
	if s := strings.Join(buildDockerArgs(d, arm, "z", "/d"), " "); !strings.Contains(s, "-arm64_shape=c3-standard-4") {
		t.Errorf("arm shape flag wrong: %s", s)
	}

	sv := Job{ID: "img_c3-highmem-176_shapevalidation", Suite: "shapevalidation", Family: "C3", Arch: "x86", Timeout: Duration(30 * time.Minute)}
	s := strings.Join(buildDockerArgs(d, sv, "z", "/d"), " ")
	if !strings.Contains(s, "-shapevalidation_test_filter ^C3$") || strings.Contains(s, "_shape=") {
		t.Errorf("sv args wrong: %s", s)
	}
}

func writeJobDir(t *testing.T, junit, logText string) string {
	t.Helper()
	dir := t.TempDir()
	if junit != "" {
		os.WriteFile(filepath.Join(dir, "junit.xml"), []byte(junit), 0o644)
	}
	if logText != "" {
		os.WriteFile(filepath.Join(dir, "job.log"), []byte(logText), 0o644)
	}
	return dir
}

func TestClassifyResult(t *testing.T) {
	pass := `<?xml version="1.0"?><testsuites><testsuite><testcase name="t"/></testsuite></testsuites>`
	fail := `<testsuites><testsuite><testcase name="t"><failure message="assert broke"/></testcase></testsuite></testsuites>`
	stockoutJunit := `<testsuites><testsuite><testcase name="t"><failure message="x"><![CDATA[Code: ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS]]></failure></testcase></testsuite></testsuites>`

	cases := []struct {
		name, junit, log string
		err              error
		timedOut         bool
		want             string
	}{
		{"pass", pass, "", nil, false, ClassPass},
		{"real failure", fail, "log noise", nil, false, ClassFail},
		{"stockout in junit", stockoutJunit, "", errors.New("exit 1"), false, ClassStockout},
		{"quota in log no junit", "", "operation failed: QUOTA_EXCEEDED: too much", errors.New("exit 1"), false, ClassQuota},
		{"stockout in log no junit", "", "err INSUFFICIENT_CAPACITY somewhere", errors.New("exit 1"), false, ClassStockout},
		{"timeout wins", "", "whatever", errors.New("killed"), true, ClassTimeout},
		{"no junit no codes", "", "manager panicked", errors.New("exit 2"), false, ClassError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyResult(c.err, writeJobDir(t, c.junit, c.log), c.timedOut)
			if got.Classification != c.want {
				t.Errorf("= %q (%s), want %q", got.Classification, got.Detail, c.want)
			}
		})
	}
}
