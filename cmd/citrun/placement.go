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
