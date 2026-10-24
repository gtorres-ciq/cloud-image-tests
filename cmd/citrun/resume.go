// cmd/citrun/resume.go
package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
)

type containerChecker interface {
	ContainerExists(jobID string) bool
	Attach(job Job, jobDir string) Result
}

func prepareResume(sched *Scheduler, chk containerChecker, runDir string, onlyFailed bool) int {
	st := sched.st
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
					last.End = sched.clock.Now()
					last.WallSeconds = last.End.Sub(last.Start).Seconds()
				}
				switch res.Classification {
				case ClassPass:
					rec.Status = StatusPassed
				case ClassFail:
					rec.Status = StatusFailedReal
				case ClassStockout:
					if len(rec.Attempts) > 0 {
						sched.recordStockout(rec, rec.Attempts[len(rec.Attempts)-1].Zone)
					} else {
						rec.Status = StatusQueued
					}
					if rec.Status == StatusQueued {
						requeued++
					}
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
			rec.StockoutZones = nil
			rec.StockoutRounds = 0
			delete(st.Cooldowns, "stockout|"+rec.Job.ID)
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

// dryRunResumePlan mirrors prepareResume's requeue rules without mutating
// state or attaching to containers (ContainerExists is a read-only docker ps).
func dryRunResumePlan(st *RunState, chk containerChecker, onlyFailed bool) (requeue, attach []string, infraCount, orphanCount int) {
	for _, id := range st.Order {
		rec := st.Jobs[id]
		switch rec.Status {
		case StatusQueued:
			requeue = append(requeue, id)
		case StatusRunning:
			if chk.ContainerExists(id) {
				attach = append(attach, id)
			} else {
				requeue = append(requeue, id)
				orphanCount++
			}
		case StatusFailedInfra:
			requeue = append(requeue, id)
			infraCount++
		case StatusFailedReal:
			if onlyFailed {
				requeue = append(requeue, id)
			}
		}
	}
	return requeue, attach, infraCount, orphanCount
}

func printResumeDryRun(dir string, requeue, attach []string, infraCount, orphanCount int, onlyFailed bool) {
	cmd := "resume"
	if onlyFailed {
		cmd = "rerun-failed"
	}
	fmt.Printf("%s dry-run for %s: %d jobs would run\n", cmd, dir, len(requeue))
	fmt.Printf("  %d failed_infra, %d orphaned running\n", infraCount, orphanCount)
	fmt.Printf("  %d running containers would be awaited/attached (not requeued)\n", len(attach))
	if len(requeue) > 0 {
		fmt.Println("would requeue:")
		for _, id := range requeue {
			fmt.Println(" ", id)
		}
	}
	if len(attach) > 0 {
		fmt.Println("would attach (still running):")
		for _, id := range attach {
			fmt.Println(" ", id)
		}
	}
}

func cmdResume(args []string, onlyFailed bool) int {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	refresh := fs.Bool("refresh-config", false, "refresh queued/failed job zones and CPU accounting from the run config (stop the original runner first)")
	dryRun := fs.Bool("dry-run", false, "print the requeue plan and exit without touching state")
	fs.Parse(args)
	if fs.NArg() != 1 {
		log.Print("usage: citrun resume|rerun-failed [--refresh-config] [--dry-run] <run-dir>")
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
	if *dryRun {
		if *refresh {
			log.Print("dry-run: ignoring -refresh-config")
		}
		requeue, attach, infra, orphans := dryRunResumePlan(st, &DockerExecutor{RunID: st.RunID}, onlyFailed)
		printResumeDryRun(dir, requeue, attach, infra, orphans, onlyFailed)
		return 0
	}
	home, err := userHome()
	if err != nil {
		log.Print(err)
		return 2
	}
	ex := &DockerExecutor{Project: cfg.Project, RunID: st.RunID,
		CredsDir: filepath.Join(home, ".config", "gcloud"), Image: cfg.DockerImage}
	if *refresh {
		n, backup, err := refreshRunConfig(dir, st, cfg)
		if err != nil {
			log.Printf("config refresh failed: %v", err)
			return 2
		}
		log.Printf("refreshed %d queued/failed jobs; state backup: %s", n, backup)
	}
	sched := NewScheduler(cfg, st, ex, realClock{}, dir)
	n := prepareResume(sched, ex, dir, onlyFailed)
	log.Printf("requeued %d jobs", n)
	if err := SaveState(dir, st); err != nil {
		log.Print(err)
		return 2
	}
	return executeRun(cfg, st, dir)
}
