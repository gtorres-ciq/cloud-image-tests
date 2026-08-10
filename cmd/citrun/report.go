// cmd/citrun/report.go
package main

import (
	"encoding/csv"
	"encoding/xml"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// statusOrder is the fixed rendering order for per-config status counts:
// successes first, then real/infra failures, quarantine, then in-flight
// states. It is a deliberate reading-order choice (not the map's own keys),
// so per-config lines read "passed N, failed_real N, ..." instead of an
// alphabetical shuffle. The outer map (config name -> counts) is still
// sorted by key before rendering, per the determinism requirement.
var statusOrder = []JobStatus{
	StatusPassed, StatusFailedReal, StatusFailedInfra, StatusQuarantined, StatusRunning, StatusQueued,
}

// junitDoc is the minimal shape needed to pull the first failure message out
// of a JUnit XML report.
type junitDoc struct {
	Suites []struct {
		Cases []struct {
			Name    string `xml:"name,attr"`
			Failure *struct {
				Message string `xml:"message,attr"`
			} `xml:"failure"`
		} `xml:"testcase"`
	} `xml:"testsuite"`
}

// junitFailureMessage reads runDir/jobs/<jobID>/junit.xml and returns
// "<testcase name>: <failure message>" for the first failing case, or "" if
// the file is missing, unparseable, or has no failure.
func junitFailureMessage(runDir, jobID string) string {
	raw, err := os.ReadFile(filepath.Join(runDir, "jobs", jobID, "junit.xml"))
	if err != nil {
		return ""
	}
	var doc junitDoc
	if xml.Unmarshal(raw, &doc) != nil {
		return ""
	}
	for _, s := range doc.Suites {
		for _, c := range s.Cases {
			if c.Failure != nil {
				return c.Name + ": " + c.Failure.Message
			}
		}
	}
	return ""
}

// attemptWallSum sums WallSeconds across attempts. Attach-resolved attempts
// (End/WallSeconds zero, only Result/Detail set) simply contribute 0.
func attemptWallSum(attempts []Attempt) float64 {
	var sum float64
	for _, a := range attempts {
		sum += a.WallSeconds
	}
	return sum
}

func attemptZones(attempts []Attempt) []string {
	zones := make([]string, len(attempts))
	for i, a := range attempts {
		zones[i] = a.Zone
	}
	return zones
}

// BuildReport renders the markdown report (also used as the console output)
// and the CSV export for a run. It walks st.Order exclusively so output is
// deterministic; any intermediate map aggregation (per-config counts,
// stockouts by region) sorts its keys before rendering.
func BuildReport(st *RunState, runDir string, topN int) (string, string) {
	var md strings.Builder
	fmt.Fprintf(&md, "# citrun report: %s\n\n", st.RunID)
	writePerConfigSummary(&md, st)
	writeRealFailures(&md, st, runDir)
	writeInfraFailures(&md, st)
	writeSlowestPassed(&md, st, topN)
	writeFlakes(&md, st)
	writeStockouts(&md, st)
	return md.String(), buildCSV(st)
}

func writePerConfigSummary(md *strings.Builder, st *RunState) {
	counts := map[string]map[JobStatus]int{}
	for _, id := range st.Order {
		rec := st.Jobs[id]
		cfg := rec.Job.Config
		if counts[cfg] == nil {
			counts[cfg] = map[JobStatus]int{}
		}
		counts[cfg][rec.Status]++
	}
	configs := make([]string, 0, len(counts))
	for cfg := range counts {
		configs = append(configs, cfg)
	}
	sort.Strings(configs)

	fmt.Fprintf(md, "## Per-Config Summary\n\n")
	for _, cfg := range configs {
		var parts []string
		for _, s := range statusOrder {
			if n := counts[cfg][s]; n > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", s, n))
			}
		}
		fmt.Fprintf(md, "- %s: %s\n", cfg, strings.Join(parts, ", "))
	}
	fmt.Fprintln(md)
}

func writeRealFailures(md *strings.Builder, st *RunState, runDir string) {
	fmt.Fprintf(md, "## Real Failures\n\n")
	found := false
	for _, id := range st.Order {
		rec := st.Jobs[id]
		if rec.Status != StatusFailedReal {
			continue
		}
		found = true
		msg := junitFailureMessage(runDir, id)
		if msg == "" {
			msg = "(no junit failure message found)"
		}
		fmt.Fprintf(md, "- %s (%s/%s/%s/%s): %s\n", id, rec.Job.Config, rec.Job.BaseImage, rec.Job.Shape, rec.Job.Suite, msg)
	}
	if !found {
		fmt.Fprintln(md, "none")
	}
	fmt.Fprintln(md)
}

func writeInfraFailures(md *strings.Builder, st *RunState) {
	fmt.Fprintf(md, "## Infra Failures\n\n")
	found := false
	for _, id := range st.Order {
		rec := st.Jobs[id]
		if rec.Status != StatusFailedInfra {
			continue
		}
		found = true
		var result, detail string
		if n := len(rec.Attempts); n > 0 {
			last := rec.Attempts[n-1]
			result, detail = last.Result, last.Detail
		}
		fmt.Fprintf(md, "- %s (%s/%s/%s/%s): %s %s\n", id, rec.Job.Config, rec.Job.BaseImage, rec.Job.Shape, rec.Job.Suite, result, detail)
	}
	if !found {
		fmt.Fprintln(md, "none")
	}
	fmt.Fprintln(md)
}

func writeSlowestPassed(md *strings.Builder, st *RunState, topN int) {
	type slowJob struct {
		id  string
		sum float64
	}
	var jobs []slowJob
	for _, id := range st.Order {
		rec := st.Jobs[id]
		if rec.Status != StatusPassed {
			continue
		}
		jobs = append(jobs, slowJob{id, attemptWallSum(rec.Attempts)})
	}
	// SliceStable on a slice built in Order sequence: ties keep their
	// original Order position, so the ranking is deterministic even when
	// wall-clock sums collide (e.g. multiple zero-WallSeconds jobs).
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].sum > jobs[j].sum })
	if topN >= 0 && len(jobs) > topN {
		jobs = jobs[:topN]
	}

	fmt.Fprintf(md, "## Slowest %d Passed Jobs\n\n", topN)
	if len(jobs) == 0 {
		fmt.Fprintln(md, "none")
	}
	for _, j := range jobs {
		fmt.Fprintf(md, "- %s: %.0fs\n", j.id, j.sum)
	}
	fmt.Fprintln(md)
}

func writeFlakes(md *strings.Builder, st *RunState) {
	fmt.Fprintf(md, "## Flakes\n\n")
	found := false
	for _, id := range st.Order {
		rec := st.Jobs[id]
		if rec.Status != StatusPassed || len(rec.Attempts) <= 1 {
			continue
		}
		found = true
		hist := make([]string, len(rec.Attempts))
		for i, a := range rec.Attempts {
			hist[i] = fmt.Sprintf("%s=%s", a.Zone, a.Result)
		}
		fmt.Fprintf(md, "- %s: %s\n", id, strings.Join(hist, ", "))
	}
	if !found {
		fmt.Fprintln(md, "none")
	}
	fmt.Fprintln(md)
}

func writeStockouts(md *strings.Builder, st *RunState) {
	counts := map[string]int{}
	for _, id := range st.Order {
		rec := st.Jobs[id]
		for _, a := range rec.Attempts {
			if a.Result == ClassStockout {
				counts[zoneRegion(a.Zone)]++
			}
		}
	}
	regions := make([]string, 0, len(counts))
	for r := range counts {
		regions = append(regions, r)
	}
	sort.Strings(regions)

	fmt.Fprintf(md, "## Stockouts by Region\n\n")
	if len(regions) == 0 {
		fmt.Fprintln(md, "none")
	}
	for _, r := range regions {
		fmt.Fprintf(md, "- %s: %d\n", r, counts[r])
	}
}

// buildCSV renders one row per job in st.Order. encoding/csv handles
// quoting so a comma or quote inside a human-written quarantine reason or a
// regex-extracted attempt detail can't corrupt the column count.
func buildCSV(st *RunState) string {
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	w.Write([]string{"config", "image", "shape", "suite", "status", "attempts", "wall_seconds", "zones", "reason"})
	for _, id := range st.Order {
		rec := st.Jobs[id]
		wall := int(math.Round(attemptWallSum(rec.Attempts)))
		zones := strings.Join(attemptZones(rec.Attempts), " ")
		var reason string
		switch rec.Status {
		case StatusQuarantined:
			reason = rec.Job.Quarantined
		case StatusFailedReal, StatusFailedInfra:
			if n := len(rec.Attempts); n > 0 {
				reason = rec.Attempts[n-1].Detail
			}
		}
		w.Write([]string{
			rec.Job.Config, rec.Job.BaseImage, rec.Job.Shape, rec.Job.Suite,
			string(rec.Status), strconv.Itoa(len(rec.Attempts)), strconv.Itoa(wall), zones, reason,
		})
	}
	w.Flush()
	return sb.String()
}

// writeReportFiles renders the report and writes report.md and report.csv
// into runDir.
func writeReportFiles(runDir string, st *RunState, topN int) error {
	md, csvOut := BuildReport(st, runDir, topN)
	if err := os.WriteFile(filepath.Join(runDir, "report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, "report.csv"), []byte(csvOut), 0o644)
}

// cmdReport implements `citrun report [-top N] <run-dir>`: it loads state,
// prints the markdown report to stdout, and writes report.md/report.csv.
func cmdReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	topN := fs.Int("top", 15, "number of slowest passed jobs to list")
	fs.Parse(args)
	if fs.NArg() != 1 {
		log.Print("usage: citrun report [-top N] <run-dir>")
		return 2
	}
	dir := fs.Arg(0)
	st, err := LoadState(dir)
	if err != nil {
		log.Print(err)
		return 2
	}
	md, _ := BuildReport(st, dir, *topN)
	fmt.Println(md)
	if err := writeReportFiles(dir, st, *topN); err != nil {
		log.Print(err)
		return 2
	}
	return 0
}
