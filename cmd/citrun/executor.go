// cmd/citrun/executor.go
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

const (
	ClassPass     = "pass"
	ClassFail     = "fail"
	ClassStockout = "stockout"
	ClassQuota    = "quota"
	ClassTimeout  = "timeout"
	ClassError    = "error"
	ClassInfra    = "infra"
)

const hardTimeoutSlack = 10 * time.Minute

type Result struct {
	Classification, Detail string
}

type Executor interface {
	Run(ctx context.Context, job Job, zone, jobDir string) Result
}

type DockerExecutor struct {
	Project, RunID, CredsDir, Image string
	// CustomStartupScript is a path to a file copied into each job dir and
	// passed to the manager via -custom_startup_script (e.g. the kernel-test
	// startup script that updates test VMs to the kernel-under-test).
	CustomStartupScript string
}

// newDockerExecutor returns an executor with CredsDir set when user ADC
// credentials exist. With no ADC file, CredsDir is left empty and
// buildDockerArgs omits the creds mount so the manager authenticates via the
// GCE metadata server (requires running on an instance with a service
// account that can create the test resources).
func newDockerExecutor(project, runID, image, customStartup string) *DockerExecutor {
	home, err := userHome()
	if err == nil {
		adc := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
		if _, statErr := os.Stat(adc); statErr == nil {
			return &DockerExecutor{Project: project, RunID: runID,
				CredsDir: filepath.Dir(adc), Image: image, CustomStartupScript: customStartup}
		}
	}
	log.Print("no ADC credentials found; using GCE metadata server auth " +
		"(requires an instance service account with compute+storage access)")
	return &DockerExecutor{Project: project, RunID: runID, Image: image,
		CustomStartupScript: customStartup}
}

func containerName(runID, jobID string) string {
	return "citrun-" + runID + "-" + jobID
}

func buildDockerArgs(d *DockerExecutor, job Job, zone, jobDir string) []string {
	if absolute, err := filepath.Abs(jobDir); err == nil {
		jobDir = absolute
	}
	args := []string{"run", "--rm", "--name", containerName(d.RunID, job.ID),
		"-v", jobDir + ":/curpath:z",
		// Serial-log policy inside manager: CIT_SERIAL_ONCE=1 (default) does
		// one serial capture+upload per VM at VM stop instead of daisy's
		// every-3s full-log rewrite (measured ~83 GB/hour upload at 30
		// containers). Propagate the host's setting so it's toggleable, and
		// point ARTIFACTS at the jobDir so the serial log lands next to
		// junit.xml on the host.
		"-e", "CIT_SERIAL_ONCE=" + os.Getenv("CIT_SERIAL_ONCE"),
		"-e", "ARTIFACTS=/curpath"}
	if d.CredsDir != "" {
		// User ADC credentials mounted from the host.
		args = append(args,
			"-v", d.CredsDir+":/creds:z",
			"-e", "GOOGLE_APPLICATION_CREDENTIALS=/creds/application_default_credentials.json")
	} else {
		// Metadata-server auth: 169.254.169.254 is link-local on the host
		// network and unreachable from podman's bridge, so join it. The
		// manager container opens no listening ports.
		args = append(args, "--network", "host")
	}
	args = append(args,
		d.Image,
		"--project", d.Project,
		"--filter", "^("+job.Suite+")$",
		"--zones", zone,
		"--images", job.Image,
		"--parallel_count", "1",
		"--out_path", "/curpath/junit.xml",
		"--timeout", time.Duration(job.Timeout).String(),
	)
	switch {
	case job.Suite == "shapevalidation":
		args = append(args, "-shapevalidation_test_filter", "^"+job.Family+"$")
	case job.Arch == "arm64":
		args = append(args, "-arm64_shape="+job.Shape)
	default:
		args = append(args, "-x86_shape="+job.Shape)
	}
	if d.CustomStartupScript != "" {
		args = append(args, "-custom_startup_script", "/curpath/startup.sh")
	}
	return args
}

// Same codes the manager's own retry matches (testworkflow.go:1564-1566),
// plus GCE API rate limiting ("Rate exceeded", "Rate Limit Exceeded", gRPC
// ResourceExhausted, HTTP 429). Without this, a rate-limited run classifies
// as a 2-strike error instead of a 4-strike quota hit with region cooldown.
// Bare "429" is deliberately not matched: junit duration fields like
// time="1.429" would false-positive and mask real failures.
var (
	stockoutRe = regexp.MustCompile(`ZONE_RESOURCE_POOL_EXHAUSTED|INSUFFICIENT_CAPACITY|RESOURCE_POOL_EXHAUSTED`)
	quotaRe    = regexp.MustCompile(`(?i)QUOTA_EXCEEDED|RATE.?EXCEEDED|RATE.?LIMIT|HTTP.?429|RESOURCE_EXHAUSTED`)
	apiErrRe   = regexp.MustCompile(
		`googleapi: Error \d{3}|resourceInUseByAnotherResource|HttpErrorStatusCode:\d{3}|` +
			`SERVICE UNAVAILABLE|googleapi: got HTTP response code 5\d\d|http2: server sent GOAWAY`)
)

func firstMatch(b []byte, re *regexp.Regexp) string {
	if loc := re.FindIndex(b); loc != nil {
		end := loc[1] + 80
		if end > len(b) {
			end = len(b)
		}
		return string(b[loc[0]:end])
	}
	return ""
}

// allFailureBlocksMatch splits junit.xml on each <failure element and reports
// whether every failure block (from its <failure tag to the next failure or
// end of document) contains a match of re. Testcases without a failure
// contribute no block, so they cannot mask an infra classification.
func allFailureBlocksMatch(junit []byte, re *regexp.Regexp) bool {
	idx := bytes.Split(junit, []byte("<failure"))
	blocks := idx[1:]
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if !re.Match(b) {
			return false
		}
	}
	return true
}

func classifyResult(runErr error, jobDir string, timedOut bool) Result {
	junit, _ := os.ReadFile(filepath.Join(jobDir, "junit.xml"))
	logb, _ := os.ReadFile(filepath.Join(jobDir, "job.log"))
	if timedOut {
		return Result{ClassTimeout, "hard timeout backstop hit"}
	}
	if len(junit) > 0 {
		if !bytes.Contains(junit, []byte("<failure")) {
			return Result{ClassPass, ""}
		}
		if quotaRe.Match(junit) {
			return Result{ClassQuota, firstMatch(junit, quotaRe)}
		}
		if stockoutRe.Match(junit) {
			return Result{ClassStockout, firstMatch(junit, stockoutRe)}
		}
		// Infra only when EVERY failure block is an API error; one genuine
		// assertion failure anywhere keeps the job a real failure.
		if allFailureBlocksMatch(junit, apiErrRe) {
			return Result{ClassInfra, firstMatch(junit, apiErrRe)}
		}
		return Result{ClassFail, ""}
	}
	if quotaRe.Match(logb) {
		return Result{ClassQuota, firstMatch(logb, quotaRe)}
	}
	if stockoutRe.Match(logb) {
		return Result{ClassStockout, firstMatch(logb, stockoutRe)}
	}
	detail := "no junit produced"
	if runErr != nil {
		detail = fmt.Sprintf("no junit produced: %v", runErr)
	}
	return Result{ClassError, detail}
}

// stageStartupScript copies the custom startup script into the mounted job
// dir so the manager can read it at /curpath/startup.sh. Re-copied on every
// Run, so requeued jobs after a resume always get the current script.
func stageStartupScript(path, jobDir string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read custom startup script: %w", err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "startup.sh"), src, 0o755); err != nil {
		return fmt.Errorf("stage custom startup script: %w", err)
	}
	return nil
}

func (d *DockerExecutor) Run(ctx context.Context, job Job, zone, jobDir string) Result {
	logf, err := os.Create(filepath.Join(jobDir, "job.log"))
	if err != nil {
		return Result{ClassError, err.Error()}
	}
	defer logf.Close()

	if d.CustomStartupScript != "" {
		if err := stageStartupScript(d.CustomStartupScript, jobDir); err != nil {
			return Result{ClassError, err.Error()}
		}
	}

	hard := time.Duration(job.Timeout) + hardTimeoutSlack
	cctx, cancel := context.WithTimeout(ctx, hard)
	defer cancel()

	cmd := exec.Command("docker", buildDockerArgs(d, job, zone, jobDir)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return Result{ClassError, "docker start: " + err.Error()}
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	var runErr error
	timedOut := false
	select {
	case runErr = <-waitErr:
	case <-cctx.Done():
		timedOut = cctx.Err() == context.DeadlineExceeded
		// CommandContext would only kill the docker CLI; kill the container.
		exec.Command("docker", "kill", containerName(d.RunID, job.ID)).Run()
		runErr = <-waitErr
	}
	return classifyResult(runErr, jobDir, timedOut)
}

// Attach re-joins a container that survived an orchestrator crash: block
// until it exits, then classify its artifacts.
func (d *DockerExecutor) Attach(job Job, jobDir string) Result {
	exec.Command("docker", "wait", containerName(d.RunID, job.ID)).Run()
	return classifyResult(nil, jobDir, false)
}

func (d *DockerExecutor) ContainerExists(jobID string) bool {
	out, err := exec.Command("docker", "ps", "-q", "--filter", "name=^"+containerName(d.RunID, jobID)+"$").Output()
	return err == nil && len(bytes.TrimSpace(out)) > 0
}
