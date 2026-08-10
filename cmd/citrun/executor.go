// cmd/citrun/executor.go
package main

import (
	"bytes"
	"context"
	"fmt"
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
}

func containerName(runID, jobID string) string {
	return "citrun-" + runID + "-" + jobID
}

func buildDockerArgs(d *DockerExecutor, job Job, zone, jobDir string) []string {
	args := []string{"run", "--rm", "--name", containerName(d.RunID, job.ID),
		"-v", jobDir + ":/curpath:z",
		"-v", d.CredsDir + ":/creds:z",
		"-e", "GOOGLE_APPLICATION_CREDENTIALS=/creds/application_default_credentials.json",
		d.Image,
		"--project", d.Project,
		"--filter", "^(" + job.Suite + ")$",
		"--zones", zone,
		"--images", job.Image,
		"--parallel_count", "1",
		"--out_path", "/curpath/junit.xml",
		"--timeout", time.Duration(job.Timeout).String(),
	}
	switch {
	case job.Suite == "shapevalidation":
		args = append(args, "-shapevalidation_test_filter", "^"+job.Family+"$")
	case job.Arch == "arm64":
		args = append(args, "-arm64_shape="+job.Shape)
	default:
		args = append(args, "-x86_shape="+job.Shape)
	}
	return args
}

// Same codes the manager's own retry matches (testworkflow.go:1443).
var (
	stockoutRe = regexp.MustCompile(`ZONE_RESOURCE_POOL_EXHAUSTED|INSUFFICIENT_CAPACITY|RESOURCE_POOL_EXHAUSTED`)
	quotaRe    = regexp.MustCompile(`QUOTA_EXCEEDED`)
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

func (d *DockerExecutor) Run(ctx context.Context, job Job, zone, jobDir string) Result {
	logf, err := os.Create(filepath.Join(jobDir, "job.log"))
	if err != nil {
		return Result{ClassError, err.Error()}
	}
	defer logf.Close()

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
