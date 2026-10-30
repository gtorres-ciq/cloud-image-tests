// cmd/citrun/executor_test.go
package main

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jstemmer/go-junit-report/v2/junit"
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

func TestDockerArgsUseAbsoluteJobDirectory(t *testing.T) {
	d := &DockerExecutor{Project: "p", RunID: "r1", CredsDir: "/home/u/.config/gcloud", Image: "cloud-image-tests"}
	job := Job{ID: "j1", Suite: "ssh", Timeout: Duration(time.Minute)}
	absolute, err := filepath.Abs("runs/r1/jobs/j1")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(buildDockerArgs(d, job, "europe-west1-b", "runs/r1/jobs/j1"), " ")
	if !strings.Contains(args, "-v "+absolute+":/curpath:z") {
		t.Fatalf("Docker args must mount an absolute job directory, got %q", args)
	}
}

func TestBuildDockerArgsMetadataCreds(t *testing.T) {
	// Empty CredsDir = metadata-server auth: no creds mount/env, host network
	// so the link-local metadata server is reachable.
	d := &DockerExecutor{Project: "p", RunID: "r1", Image: "cloud-image-tests"}
	job := Job{ID: "j1", Suite: "ssh", Timeout: Duration(time.Minute)}
	got := strings.Join(buildDockerArgs(d, job, "z", "/d"), " ")
	if !strings.Contains(got, "--network host") {
		t.Errorf("metadata mode must add --network host, got: %s", got)
	}
	for _, unwanted := range []string{"/creds", "GOOGLE_APPLICATION_CREDENTIALS"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("metadata mode must not contain %q, got: %s", unwanted, got)
		}
	}
}

func TestBuildDockerArgsCustomStartupScript(t *testing.T) {
	d := &DockerExecutor{Project: "p", RunID: "r1", CredsDir: "/home/u/.config/gcloud",
		Image: "cloud-image-tests", CustomStartupScript: "/somewhere/startup.sh"}
	job := Job{ID: "j1", Suite: "ssh", Timeout: Duration(time.Minute)}
	got := strings.Join(buildDockerArgs(d, job, "z", "/d"), " ")
	if !strings.Contains(got, "-custom_startup_script /curpath/startup.sh") {
		t.Errorf("custom startup script not passed to manager, got: %s", got)
	}

	d.CustomStartupScript = ""
	if s := strings.Join(buildDockerArgs(d, job, "z", "/d"), " "); strings.Contains(s, "custom_startup_script") {
		t.Errorf("no startup script must omit the manager flag, got: %s", s)
	}
}

func TestNewDockerExecutorADCDetection(t *testing.T) {
	// Fake HOME with an ADC file => ADC mode.
	home := t.TempDir()
	gcloudDir := filepath.Join(home, ".config", "gcloud")
	if err := os.MkdirAll(gcloudDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	userHomeOrig := userHome
	userHome = func() (string, error) { return home, nil }
	defer func() { userHome = userHomeOrig }()

	if err := os.WriteFile(filepath.Join(gcloudDir, "application_default_credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := newDockerExecutor("p", "r1", "img", "")
	if d.CredsDir != gcloudDir {
		t.Errorf("ADC present: want CredsDir %q, got %q", gcloudDir, d.CredsDir)
	}

	// No ADC file => metadata mode (empty CredsDir).
	if err := os.Remove(filepath.Join(gcloudDir, "application_default_credentials.json")); err != nil {
		t.Fatal(err)
	}
	d = newDockerExecutor("p", "r1", "img", "/startup.sh")
	if d.CredsDir != "" {
		t.Errorf("no ADC: want empty CredsDir, got %q", d.CredsDir)
	}
	if d.CustomStartupScript != "/startup.sh" {
		t.Errorf("CustomStartupScript not propagated, got %q", d.CustomStartupScript)
	}
}

func TestStageStartupScript(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "orig.sh")
	script := "#!/bin/bash\ndnf config-manager --set-enabled ciq-sigcloud-next-testing\n"
	if err := os.WriteFile(src, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(dir, "job")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := stageStartupScript(src, jobDir); err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadFile(filepath.Join(jobDir, "startup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(staged) != script {
		t.Errorf("staged script content mismatch: %q", string(staged))
	}

	// Missing source errors clearly.
	if err := stageStartupScript(filepath.Join(dir, "nope.sh"), jobDir); err == nil {
		t.Error("staging a missing script must fail")
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
	fingerprint412 := `<testsuites><testsuite><testcase name="t"><failure message="Failed"><![CDATA[image_ssh_test.go:117: failed to set instance metadata: googleapi: Error 412: Supplied fingerprint does not match current metadata fingerprint., conditionNotMet]]></failure></testcase></testsuite></testsuites>`
	resourceInUse := `<testsuites><testsuite><testcase name="t"><failure message="Failed"><![CDATA[googleapi: Error 400: The disk resource 'disk' is already being used by 'instance', resourceInUseByAnotherResource]]></failure></testcase></testsuite></testsuites>`
	gcsGoaway := `<testsuites><testsuite><testcase name="t"><failure message="" type="Failure"><![CDATA[Post "https://storage.googleapis.com/upload/storage/v1/b/bucket/o": http2: server sent GOAWAY and closed the connection; LastStreamID=29, ErrCode=NO_ERROR, debug="server_shutting_down"]]></failure></testcase></testsuite></testsuites>`
	daisy503 := `<testsuites><testsuite><testcase name="t"><failure message="" type="Failure"><![CDATA[step "create-disks" run error: operation failed &{HttpErrorMessage:SERVICE UNAVAILABLE HttpErrorStatusCode:503 OperationType:insert}]]></failure></testcase></testsuite></testsuites>`
	compute502 := `<testsuites><testsuite><testcase name="t"><failure message="" type="Failure"><![CDATA[googleapi: got HTTP response code 502 with body: <!DOCTYPE html>]]></failure></testcase></testsuite></testsuites>`
	mixedInfraAndReal := `<testsuites><testsuite>` +
		`<testcase name="a"><failure message="Failed"><![CDATA[googleapi: Error 412: conditionNotMet]]></failure></testcase>` +
		`<testcase name="b"><failure message="Failed"><![CDATA[error pinging google.com on nic eth0, 40% packet loss]]></failure></testcase>` +
		`</testsuite></testsuites>`
	cleanupNotFoundLog := "serial-once: deleting VM vm1: googleapi: Error 404: not found, notFound (cleanerupper will retry)"

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
		{"rate limit in log no junit", "", "ERROR: (gcloud.compute.instances.create) Rate exceeded, enable billing or pay a fee", errors.New("exit 1"), false, ClassQuota},
		{"rate limit in junit", `<testsuites><testsuite><testcase name="t"><failure message="x"><![CDATA[Rate Limit Exceeded]]></failure></testcase></testsuite></testsuites>`, "", errors.New("exit 1"), false, ClassQuota},
		{"grpc 429 in log no junit", "", `rpc error: code = ResourceExhausted desc = Rate Exceeded.`, errors.New("exit 1"), false, ClassQuota},
		{"junit duration 429 is not rate limit", `<testsuites><testsuite><testcase name="t" time="1.429"><failure message="assert broke"/></testcase></testsuite></testsuites>`, "", nil, false, ClassFail},
		{"stockout in log no junit", "", "err INSUFFICIENT_CAPACITY somewhere", errors.New("exit 1"), false, ClassStockout},
		{"timeout wins", "", "whatever", errors.New("killed"), true, ClassTimeout},
		{"no junit no codes", "", "manager panicked", errors.New("exit 2"), false, ClassError},
		{"metadata fingerprint 412 in junit", fingerprint412, "", nil, false, ClassInfra},
		{"resource in use in junit", resourceInUse, "", errors.New("exit 1"), false, ClassInfra},
		{"gcs goaway in junit", gcsGoaway, "", errors.New("exit 1"), false, ClassInfra},
		{"daisy 503 in junit", daisy503, "", errors.New("exit 1"), false, ClassInfra},
		{"compute api 502 in junit", compute502, "", errors.New("exit 1"), false, ClassInfra},
		{"mixed api error and real failure", mixedInfraAndReal, "", nil, false, ClassFail},
		{"cleanup notFound in log does not mask real failure", fail, cleanupNotFoundLog, nil, false, ClassFail},
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

func TestMarkInfraSkipped(t *testing.T) {
	stockout := `<testsuites><testsuite tests="3" failures="2" errors="1" skipped="0">` +
		`<testcase name="a"><failure message="" type="Failure"><![CDATA[Code: ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS]]></failure></testcase>` +
		`<testcase name="b"><failure message="" type="Failure"><![CDATA[Code: ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS]]></failure></testcase>` +
		`<testcase name="c"><error message="boom"><![CDATA[SERVICE UNAVAILABLE]]></error></testcase>` +
		`</testsuite></testsuites>`
	realFail := `<testsuites><testsuite tests="1" failures="1" errors="0">` +
		`<testcase name="a"><failure message="assert broke"/></testcase>` +
		`</testsuite></testsuites>`
	timeout := `<testsuites><testsuite tests="1" failures="1" errors="0">` +
		`<testcase name="a"><failure message="hang"/></testcase>` +
		`</testsuite></testsuites>`
	pass := `<testsuites><testsuite tests="1" failures="0" errors="0">` +
		`<testcase name="a"/></testsuite></testsuites>`

	cases := []struct {
		name, junit string
		res         Result
		wantParse   bool   // whether the file should still parse as junit
		wantContent string // expected substring of the rewritten file ("" = untouched)
	}{
		{"stockout failures become skipped", stockout,
			Result{Classification: ClassStockout, Detail: "ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS Message: zone stockout"},
			true, `skipped message="infra (stockout): ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS Message: zone stockout"`},
		{"quota failures become skipped", realFail,
			Result{Classification: ClassQuota, Detail: "QUOTA_EXCEEDED"},
			true, `skipped message="infra (quota): QUOTA_EXCEEDED"`},
		{"infra failures become skipped", realFail,
			Result{Classification: ClassInfra, Detail: "googleapi: Error 412: conditionNotMet"},
			true, `skipped message="infra (infra): googleapi: Error 412: conditionNotMet"`},
		{"real failures untouched", realFail,
			Result{Classification: ClassFail, Detail: ""},
			true, `<failure message="assert broke"/>`},
		{"timeout untouched", timeout,
			Result{Classification: ClassTimeout, Detail: "hard timeout"},
			true, `<failure message="hang"/>`},
		{"error untouched", timeout,
			Result{Classification: ClassError, Detail: "boom"},
			true, `<failure message="hang"/>`},
		{"pass untouched", pass,
			Result{Classification: ClassPass, Detail: ""},
			true, `<testcase name="a"/>`},
		{"malformed junit left as-is", `<testsuites><testsuite><oops>`,
			Result{Classification: ClassStockout, Detail: "x"},
			false, `<oops>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeJobDir(t, c.junit, "")
			markInfraSkipped(dir, c.res)
			raw, err := os.ReadFile(filepath.Join(dir, "junit.xml"))
			if err != nil {
				t.Fatalf("read rewritten junit: %v", err)
			}
			if !strings.Contains(string(raw), c.wantContent) {
				t.Errorf("rewritten junit missing %q:\n%s", c.wantContent, raw)
			}
			if !c.wantParse {
				return
			}
			var suites junit.Testsuites
			if err := xml.Unmarshal(raw, &suites); err != nil {
				t.Fatalf("rewritten junit does not parse: %v\n%s", err, raw)
			}
			for _, suite := range suites.Suites {
				wantSkipped := 0
				for _, tc := range suite.Testcases {
					if tc.Skipped != nil {
						wantSkipped++
					}
					if tc.Failure != nil && c.res.Classification != ClassFail && c.res.Classification != ClassTimeout && c.res.Classification != ClassError {
						t.Errorf("failure survived rewrite: %s", tc.Name)
					}
				}
				if suite.Skipped != wantSkipped {
					t.Errorf("suite skipped count = %d, want %d", suite.Skipped, wantSkipped)
				}
				if suite.Failures < 0 || suite.Errors < 0 {
					t.Errorf("suite counts negative: failures=%d errors=%d", suite.Failures, suite.Errors)
				}
			}
		})
	}
}

func TestAllFailureBlocksMatch(t *testing.T) {
	infra := `<testsuites><testsuite>` +
		`<testcase name="a"><failure message="x"><![CDATA[googleapi: Error 412: conditionNotMet]]></failure></testcase>` +
		`<testcase name="b"><failure message="y"><![CDATA[HttpErrorStatusCode:503 SERVICE UNAVAILABLE]]></failure></testcase>` +
		`</testsuite></testsuites>`
	mixed := `<testsuites><testsuite>` +
		`<testcase name="a"><failure message="x"><![CDATA[googleapi: Error 412: conditionNotMet]]></failure></testcase>` +
		`<testcase name="b"><failure message="y"><![CDATA[error pinging google.com, 40% packet loss]]></failure></testcase>` +
		`</testsuite></testsuites>`
	cases := []struct {
		name, junit string
		want        bool
	}{
		{"all blocks are api errors", infra, true},
		{"one real failure among api errors", mixed, false},
		{"no failure blocks", `<testsuites><testsuite><testcase name="a"/></testsuite></testsuites>`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := allFailureBlocksMatch([]byte(c.junit), apiErrRe); got != c.want {
				t.Errorf("= %v, want %v", got, c.want)
			}
		})
	}
}
