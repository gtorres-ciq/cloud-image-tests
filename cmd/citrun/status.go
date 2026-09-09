// cmd/citrun/status.go
package main

import (
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// statusCounts tallies st.Jobs by JobStatus and renders each non-zero count
// as "<status> <n>", in statusOrder (report.go) for deterministic,
// human-friendly output — reusing the same fixed reading order the markdown
// report uses instead of an alphabetical map-key shuffle.
func statusCounts(st *RunState) []string {
	counts := map[JobStatus]int{}
	for _, id := range st.Order {
		counts[st.Jobs[id].Status]++
	}
	var parts []string
	for _, s := range statusOrder {
		if n := counts[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", s, n))
		}
	}
	return parts
}

// regionOccupancy derives per-region occupancy from state alone: for every
// job whose LAST attempt is still open (Result == "running"), its CPUCost is
// added to that attempt's zoneRegion. A job's earlier, already-resolved
// attempts (retries) never count. Returned regions are sorted for
// deterministic rendering.
func regionOccupancy(st *RunState) (regions []string, runningPerRegion, cpusPerRegion map[string]int) {
	runningPerRegion, cpusPerRegion = map[string]int{}, map[string]int{}
	for _, id := range st.Order {
		rec := st.Jobs[id]
		n := len(rec.Attempts)
		if n == 0 || rec.Attempts[n-1].Result != "running" {
			continue
		}
		region := zoneRegion(rec.Attempts[n-1].Zone)
		if runningPerRegion[region] == 0 {
			regions = append(regions, region)
		}
		runningPerRegion[region]++
		cpusPerRegion[region] += rec.Job.CPUCost
	}
	sort.Strings(regions)
	return regions, runningPerRegion, cpusPerRegion
}

// activeCooldowns renders every not-yet-expired cooldown as "<key> <n>s"
// remaining, sorted by key. Expired cooldowns (now at or past until) are
// omitted — a stale entry lingering in state.json until the scheduler next
// touches that series/region shouldn't be reported as active.
func activeCooldowns(st *RunState, now time.Time) []string {
	var keys []string
	for k := range st.Cooldowns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		if until := st.Cooldowns[k]; now.Before(until) {
			out = append(out, fmt.Sprintf("%s %ds", k, int(until.Sub(now).Seconds())))
		}
	}
	return out
}

// eta returns a rough remaining-time estimate: the summed Est of queued jobs
// divided by the number of jobs currently running (never dividing by zero),
// rounded to the second — Est sums over hundreds of queued jobs would
// otherwise render sub-second noise like "4m59.999999999s".
func eta(st *RunState) time.Duration {
	var sum time.Duration
	running := 0
	for _, id := range st.Order {
		rec := st.Jobs[id]
		switch rec.Status {
		case StatusQueued:
			sum += time.Duration(rec.Job.Est)
		case StatusRunning:
			running++
		}
	}
	if running < 1 {
		running = 1
	}
	return (sum / time.Duration(running)).Round(time.Second)
}

// RenderStatus renders the full multi-line status view for a run: counts by
// job status, per-region running-job/CPU occupancy, active cooldowns with
// remaining seconds, and a rough ETA. Everything is derived from st alone —
// no scheduler internals are consulted — so it renders identically whether
// st came from a live run's state.json or a resumed/finished one.
func RenderStatus(st *RunState, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "run %s\n\n", st.RunID)

	fmt.Fprintf(&b, "jobs: %s\n\n", strings.Join(statusCounts(st), "  "))

	fmt.Fprintln(&b, "regions:")
	regions, runningPerRegion, cpusPerRegion := regionOccupancy(st)
	if len(regions) == 0 {
		fmt.Fprintln(&b, "  none")
	}
	for _, r := range regions {
		fmt.Fprintf(&b, "  %s: %d running, %d cpus\n", r, runningPerRegion[r], cpusPerRegion[r])
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "cooldowns:")
	if cds := activeCooldowns(st, now); len(cds) == 0 {
		fmt.Fprintln(&b, "  none")
	} else {
		for _, c := range cds {
			fmt.Fprintf(&b, "  %s\n", c)
		}
	}
	fmt.Fprintln(&b)

	fmt.Fprintf(&b, "ETA ~%s\n", eta(st))
	return b.String()
}

// RenderTicker renders the same state RenderStatus does, condensed to one
// line for a periodic progress print: "queued N | running N | done N (pass P
// / real R / infra I) | ETA ~X". done/pass/real/infra count only jobs the
// scheduler actually ran; skipped_quarantine jobs never enter the scheduler
// and are intentionally excluded here (RenderStatus's per-status counts
// still show them).
func RenderTicker(st *RunState, now time.Time) string {
	counts := map[JobStatus]int{}
	for _, id := range st.Order {
		counts[st.Jobs[id].Status]++
	}
	pass, real, infra := counts[StatusPassed], counts[StatusFailedReal], counts[StatusFailedInfra]
	return fmt.Sprintf("queued %d | running %d | done %d (pass %d / real %d / infra %d) | ETA ~%s",
		counts[StatusQueued], counts[StatusRunning], pass+real+infra, pass, real, infra, eta(st))
}

// cmdStatus implements `citrun status [-watch] <run-dir>`: a one-shot print
// of RenderStatus, or with -watch a screen-clearing refresh every 5s. It
// only ever reads state.json (via LoadState) — the same file a live
// scheduler process persists to after every job transition — so it's safe
// to run alongside `citrun run` in another terminal.
func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	watch := fs.Bool("watch", false, "clear the screen and refresh every 5s")
	fs.Parse(args)
	if fs.NArg() != 1 {
		log.Print("usage: citrun status [-watch] <run-dir>")
		return 2
	}
	dir := fs.Arg(0)

	for {
		st, err := LoadState(dir)
		if err != nil {
			log.Print(err)
			return 2
		}
		if *watch {
			fmt.Print("\033[2J\033[H")
		}
		fmt.Println(RenderStatus(st, time.Now()))
		if !*watch {
			return 0
		}
		time.Sleep(5 * time.Second)
	}
}
