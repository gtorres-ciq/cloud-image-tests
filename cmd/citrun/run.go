// cmd/citrun/run.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func splitGlobs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", "citrun.yaml", "config file")
	configs := fs.String("configs", "", "comma-separated config name globs")
	images := fs.String("images", "", "comma-separated image globs")
	shapes := fs.String("shapes", "", "comma-separated shape globs")
	suites := fs.String("suites", "", "comma-separated suite globs")
	runDir := fs.String("run-dir", "", "run directory (default runs/<timestamp>)")
	dryRun := fs.Bool("dry-run", false, "print the expanded plan and exit")
	cleanup := fs.Bool("cleanup", true, "sweep leaked resources before and after")
	fs.Parse(args)

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Print(err)
		return 2
	}
	filter := JobFilter{Configs: splitGlobs(*configs), Images: splitGlobs(*images),
		Shapes: splitGlobs(*shapes), Suites: splitGlobs(*suites)}
	jobs, err := ExpandJobs(cfg, filter)
	if err != nil {
		log.Print(err)
		return 2
	}
	if err := validatePlaceable(cfg, jobs); err != nil {
		log.Print(err)
		return 2
	}
	if *dryRun {
		printPlan(jobs)
		return 0
	}

	dir := *runDir
	if dir == "" {
		dir = filepath.Join("runs", time.Now().Format("20060102-150405"))
	}
	if err := runDirFresh(dir); err != nil {
		log.Print(err)
		return 2
	}
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o755); err != nil {
		log.Print(err)
		return 2
	}
	raw, err := os.ReadFile(*configPath)
	if err != nil {
		log.Print(err)
		return 2
	}
	if err := os.WriteFile(filepath.Join(dir, "citrun.yaml"), raw, 0o644); err != nil {
		log.Print(err)
		return 2
	}

	runID := filepath.Base(dir)
	st := NewRunState(runID, time.Now(), jobs)
	if err := SaveState(dir, st); err != nil {
		log.Print(err)
		return 2
	}
	if *cleanup {
		if err := sweep(cfg.Project, "2h"); err != nil {
			log.Printf("WARN: cleanup failed (continuing): %v", err)
		}
	}
	code := executeRun(cfg, st, dir)
	if *cleanup {
		if err := sweep(cfg.Project, "2h"); err != nil {
			log.Printf("WARN: cleanup failed (continuing): %v", err)
		}
	}
	return code
}

// runDirFresh rejects a run dir that already holds a state.json — that is a
// prior run's record; use `citrun resume` instead of overwriting it.
func runDirFresh(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err == nil {
		return fmt.Errorf("%s already contains state.json (a prior run) — use `citrun resume %s` or pick a new -run-dir", dir, dir)
	}
	return nil
}

func userHome() (string, error) {
	return os.UserHomeDir()
}

func executeRun(cfg *Config, st *RunState, dir string) int {
	home, err := userHome()
	if err != nil {
		log.Print(err)
		return 2
	}
	ex := &DockerExecutor{Project: cfg.Project, RunID: st.RunID,
		CredsDir: filepath.Join(home, ".config", "gcloud"), Image: cfg.DockerImage}
	sched := NewScheduler(cfg, st, ex, realClock{}, dir)

	admitCtx, stopAdmit := context.WithCancel(context.Background())
	jobCtx, killJobs := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		log.Print("interrupt: draining (no new jobs; Ctrl-C again to kill running containers)")
		stopAdmit()
		<-sig
		log.Print("second interrupt: killing containers")
		killJobs()
	}()

	stopTicker := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopTicker:
				return
			case <-time.After(30 * time.Second):
				if snap, err := LoadState(dir); err == nil {
					fmt.Println(RenderTicker(snap, time.Now()))
				}
			}
		}
	}()

	err = sched.Run(admitCtx, jobCtx)
	close(stopTicker)
	signal.Stop(sig)
	if err != nil {
		log.Printf("scheduler: %v", err)
		return 2
	}
	final, err := LoadState(dir)
	if err != nil {
		log.Print(err)
		return 2
	}
	md, _ := BuildReport(final, dir, 15)
	fmt.Println(md)
	if err := writeReportFiles(dir, final, 15); err != nil {
		log.Printf("WARN: write report files: %v", err)
	}
	for _, rec := range final.Jobs {
		if rec.Status == StatusFailedReal || rec.Status == StatusFailedInfra {
			return 1
		}
	}
	return 0
}

func printPlan(jobs []Job) {
	perConfig := map[string]int{}
	perConfigQ := map[string]int{}
	var order []string
	for _, j := range jobs {
		if perConfig[j.Config] == 0 && perConfigQ[j.Config] == 0 {
			order = append(order, j.Config)
		}
		perConfig[j.Config]++
		if j.Quarantined != "" {
			perConfigQ[j.Config]++
		}
	}
	total, totalQ := 0, 0
	for _, c := range order {
		fmt.Printf("%-16s %5d jobs  (%d quarantined)\n", c, perConfig[c], perConfigQ[c])
		total += perConfig[c]
		totalQ += perConfigQ[c]
	}
	fmt.Printf("%-16s %5d jobs  (%d quarantined)\n", "TOTAL", total, totalQ)
}

const cleanupRegions = "europe-west1,europe-west4,asia-southeast1,us-central1,us-east1,us-east4,us-west1"

func sweep(project, olderThan string) error {
	log.Print("resource sweep (cmd/cleanup)...")
	cmd := exec.Command("go", "run", "./cmd/cleanup", "-project", project,
		"-older-than", olderThan, "-regions", cleanupRegions, "-no-dry-run")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
