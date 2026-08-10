// cmd/citrun/resume.go
package main

import (
	"flag"
	"log"
	"path/filepath"
)

type containerChecker interface {
	ContainerExists(jobID string) bool
	Attach(job Job, jobDir string) Result
}

func prepareResume(st *RunState, chk containerChecker, runDir string, onlyFailed bool) int {
	requeued := 0
	for _, id := range st.Order {
		rec := st.Jobs[id]
		switch rec.Status {
		case StatusQueued:
			requeued++
		case StatusRunning:
			if chk.ContainerExists(id) {
				res := chk.Attach(rec.Job, filepath.Join(runDir, "jobs", id))
				if n := len(rec.Attempts); n > 0 {
					last := &rec.Attempts[n-1]
					last.Result = res.Classification
					last.Detail = res.Detail
				}
				switch res.Classification {
				case ClassPass:
					rec.Status = StatusPassed
				case ClassFail:
					rec.Status = StatusFailedReal
				default:
					rec.Status = StatusQueued
					requeued++
				}
			} else {
				if n := len(rec.Attempts); n > 0 {
					rec.Attempts[n-1].Result = "orphaned"
				}
				rec.Status = StatusQueued
				requeued++
			}
		case StatusFailedInfra:
			rec.Status = StatusQueued
			requeued++
		case StatusFailedReal:
			if onlyFailed {
				rec.Status = StatusQueued
				requeued++
			}
		}
	}
	return requeued
}

func cmdResume(args []string, onlyFailed bool) int {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 1 {
		log.Print("usage: citrun resume|rerun-failed <run-dir>")
		return 2
	}
	dir := fs.Arg(0)
	st, err := LoadState(dir)
	if err != nil {
		log.Print(err)
		return 2
	}
	cfg, err := LoadConfig(filepath.Join(dir, "citrun.yaml"))
	if err != nil {
		log.Print(err)
		return 2
	}
	home, err := userHome()
	if err != nil {
		log.Print(err)
		return 2
	}
	ex := &DockerExecutor{Project: cfg.Project, RunID: st.RunID,
		CredsDir: filepath.Join(home, ".config", "gcloud"), Image: cfg.DockerImage}
	n := prepareResume(st, ex, dir, onlyFailed)
	log.Printf("requeued %d jobs", n)
	if err := SaveState(dir, st); err != nil {
		log.Print(err)
		return 2
	}
	return executeRun(cfg, st, dir)
}
