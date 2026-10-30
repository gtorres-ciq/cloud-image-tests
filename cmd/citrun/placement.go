// cmd/citrun/placement.go
package main

import (
	"fmt"
	"sort"
	"strings"
)

// budgetKeysFor mirrors Scheduler.budgetKeys: CPUS always, plus the job's
// BudgetKey where the region defines it.
func budgetKeysFor(cfg *Config, job Job, region string) []string {
	keys := []string{"CPUS"}
	if job.BudgetKey != "CPUS" {
		if _, ok := cfg.Budgets.Regions[region][job.BudgetKey]; ok {
			keys = append(keys, job.BudgetKey)
		}
	}
	return keys
}

// regionUsable mirrors Scheduler.allowed's base case (no runtime quota
// penalty applies at plan time): usable = int(limit * safety factor),
// minned across the job's budget keys for region.
func regionUsable(cfg *Config, job Job, region string) int {
	usable := -1
	for _, key := range budgetKeysFor(cfg, job, region) {
		limit := cfg.Budgets.Regions[region][key]
		u := int(float64(limit) * cfg.SafetyFactor)
		if usable == -1 || u < usable {
			usable = u
		}
	}
	return usable
}

// eligibleRegions returns the sorted, deduplicated regions reachable from
// job.Zones.
func eligibleRegions(job Job) []string {
	seen := map[string]bool{}
	var regions []string
	for _, z := range job.Zones {
		r := zoneRegion(z)
		if !seen[r] {
			seen[r] = true
			regions = append(regions, r)
		}
	}
	sort.Strings(regions)
	return regions
}

// ExcludedZone describes one shape×zone placement the scheduler silently
// skips: the zone's region can never fit the job's CPUCost under the
// configured budgets (mirrors fitsConfiguredBudget — live occupancy and quota
// penalties are ignored, only configured feasibility counts). Such zones are
// candidates for removal from their zone_set, or for a budget increase on the
// family key in that region.
type ExcludedZone struct {
	Shape     string
	BudgetKey string
	CPUCost   int
	Zone      string
	Region    string
	Usable    map[string]int // region's usable budget (limit × safety) per key considered
	Missing   string         // family key absent from the region's budget row, "" if present
}

// excludedZones reports the unique shape×zone placements across a run's jobs
// that the scheduler excludes from every sweep, plus the count of jobs with
// no feasible zone at all (validatePlaceable gates runs so this should stay
// zero; it is reported defensively for states built before that gate).
func excludedZones(cfg *Config, st *RunState) (excl []ExcludedZone, zeroFit int) {
	seen := map[string]bool{}
	for _, id := range st.Order {
		rec := st.Jobs[id]
		if rec == nil || rec.Job.Quarantined != "" {
			continue
		}
		j := rec.Job
		feasible := 0
		for _, z := range j.Zones {
			r := zoneRegion(z)
			if regionUsable(cfg, j, r) >= j.CPUCost {
				feasible++
				continue
			}
			key := j.Shape + "|" + z
			if seen[key] {
				continue
			}
			seen[key] = true
			e := ExcludedZone{
				Shape: j.Shape, BudgetKey: j.BudgetKey, CPUCost: j.CPUCost,
				Zone: z, Region: r, Usable: map[string]int{},
			}
			for _, k := range budgetKeysFor(cfg, j, r) {
				e.Usable[k] = int(float64(cfg.Budgets.Regions[r][k]) * cfg.SafetyFactor)
			}
			if _, ok := cfg.Budgets.Regions[r][j.BudgetKey]; !ok && j.BudgetKey != "CPUS" {
				e.Missing = j.BudgetKey
			}
			excl = append(excl, e)
		}
		if len(j.Zones) > 0 && feasible == 0 {
			zeroFit++
		}
	}
	sort.Slice(excl, func(i, k int) bool {
		if excl[i].Shape != excl[k].Shape {
			return excl[i].Shape < excl[k].Shape
		}
		return excl[i].Zone < excl[k].Zone
	})
	return excl, zeroFit
}

// validatePlaceable returns an error listing jobs that could never be admitted:
// their CPUCost exceeds every eligible region's usable budget (limit × safety,
// min across the job's budget keys). Quarantined jobs are skipped (never launch).
func validatePlaceable(cfg *Config, jobs []Job) error {
	type unplaceable struct {
		job     Job
		regions []string
		usable  map[string]int
	}
	var bad []unplaceable
	for _, j := range jobs {
		if j.Quarantined != "" {
			continue
		}
		regions := eligibleRegions(j)
		usable := make(map[string]int, len(regions))
		fits := false
		for _, r := range regions {
			u := regionUsable(cfg, j, r)
			usable[r] = u
			if u >= j.CPUCost {
				fits = true
			}
		}
		if !fits {
			bad = append(bad, unplaceable{j, regions, usable})
		}
	}
	if len(bad) == 0 {
		return nil
	}

	const shown = 5
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d job(s) can never be placed (CPUCost exceeds every eligible region's usable budget):\n", len(bad))
	for i, b := range bad {
		if i == shown {
			fmt.Fprintf(&sb, "...and %d more\n", len(bad)-shown)
			break
		}
		mins := make([]string, len(b.regions))
		for ri, r := range b.regions {
			mins[ri] = fmt.Sprintf("%s=%d", r, b.usable[r])
		}
		if len(mins) == 0 {
			mins = []string{"(no eligible regions)"}
		}
		fmt.Fprintf(&sb, "  %s: CPUCost=%d, usable minimums: %s\n", b.job.ID, b.job.CPUCost, strings.Join(mins, ", "))
	}
	return fmt.Errorf("%s", sb.String())
}
