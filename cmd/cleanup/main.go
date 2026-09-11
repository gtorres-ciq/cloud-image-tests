// Command cleanup deletes leftover cloud-image-tests resources (instances,
// disks, load-balancer resources, networks) left behind by interrupted or
// failed test runs, using the cleanerupper library (which has no CLI of its
// own).
//
// It DRY-RUNS by default (only lists what it would delete). Pass -no-dry-run to
// actually delete. Cleanup can select CIT-owned resources by age, or resources
// belonging to specific Daisy workflow IDs. The default network,
// deletion-protected instances, and resources labeled do-not-delete are always
// kept.
//
// Auth uses Application Default Credentials:
//
//	gcloud auth application-default login   # once
//	go run ./cmd/cleanup -project <project_name>                # dry-run (list only)
//	go run ./cmd/cleanup -project <project_name> -no-dry-run    # actually delete
//	go run ./cmd/cleanup -project <project_name> -workflow-ids bdg12,prqz9
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/cloud-image-tests/cleanerupper"
)

// Daisy ID characters
var workflowIDPattern = regexp.MustCompile(`^[bdghjlmnpqrstvwxyz0-9]{5}$`)

// cleanupPolicy selects workflow mode when IDs are supplied; workflow cleanup
// intentionally has no age cutoff so it can remove fresh leftovers.
func cleanupPolicy(workflowIDsCSV string, cutoff time.Time) (cleanerupper.PolicyFunc, []string, error) {
	if strings.TrimSpace(workflowIDsCSV) == "" {
		return cleanerupper.AgePolicy(cutoff), nil, nil
	}

	seen := map[string]bool{}
	var ids []string
	for _, raw := range strings.Split(workflowIDsCSV, ",") {
		id := strings.TrimSpace(raw)
		if !workflowIDPattern.MatchString(id) {
			return nil, nil, fmt.Errorf("invalid Daisy workflow ID %q (want 5 lowercase Daisy ID characters)", id)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	policies := make([]cleanerupper.PolicyFunc, 0, len(ids))
	for _, id := range ids {
		policies = append(policies, cleanerupper.WorkflowPolicy(id))
	}
	return func(resource any) bool {
		for _, policy := range policies {
			if policy(resource) {
				return true
			}
		}
		return false
	}, ids, nil
}

func main() {
	project := flag.String("project", "", "GCP project to clean (required)")
	olderThan := flag.Duration("older-than", 2*time.Hour, "only touch resources created more than this ago; keep it larger than your longest in-flight test so running VMs are spared")
	workflowIDsCSV := flag.String("workflow-ids", "", "comma-separated Daisy workflow IDs to clean instead of selecting resources by age")
	regionsCSV := flag.String("regions", "europe-west1", "comma-separated regions for load-balancer resource cleanup")
	noDryRun := flag.Bool("no-dry-run", false, "actually delete; default is a dry-run that only lists what would be deleted")
	flag.Parse()

	if *project == "" {
		log.Fatal("must provide -project")
	}
	dryRun := !*noDryRun

	ctx := context.Background()
	clients, err := cleanerupper.NewClients(ctx)
	if err != nil {
		log.Fatalf("failed to build GCP clients (is ADC set? run: gcloud auth application-default login): %v", err)
	}

	cutoff := time.Now().Add(-*olderThan)
	policy, workflowIDs, err := cleanupPolicy(*workflowIDsCSV, cutoff)
	if err != nil {
		log.Fatal(err)
	}
	regions := strings.Split(*regionsCSV, ",")

	verb := "deleted"
	if len(workflowIDs) > 0 && dryRun {
		verb = "would delete"
		log.Printf("DRY-RUN: nothing will be deleted. Resources belonging to Daisy workflow IDs [%s] in %s are eligible. Re-run with -no-dry-run to delete.", strings.Join(workflowIDs, ", "), *project)
	} else if len(workflowIDs) > 0 {
		log.Printf("DELETING resources belonging to Daisy workflow IDs [%s] in %s.", strings.Join(workflowIDs, ", "), *project)
	} else if dryRun {
		verb = "would delete"
		log.Printf("DRY-RUN: nothing will be deleted. Resources in %s created before %s (older than %s) are eligible. Re-run with -no-dry-run to delete.",
			*project, cutoff.Format(time.RFC3339), *olderThan)
	} else {
		log.Printf("DELETING resources in %s created before %s (older than %s).",
			*project, cutoff.Format(time.RFC3339), *olderThan)
	}

	report := func(kind string, cleaned []string, errs []error) {
		for _, n := range cleaned {
			log.Printf("  [%s] %s", kind, n)
		}
		for _, e := range errs {
			log.Printf("  [%s] error: %v", kind, e)
		}
		log.Printf("%s: %s %d resource(s)", kind, verb, len(cleaned))
	}

	// Order matters: instances first (they hold disks and back LB/NEGs), then
	// disks, then load-balancer resources, then networks (must be empty last).
	c := *clients
	cleaned, errs := cleanerupper.CleanInstances(c, *project, policy, dryRun)
	report("instances", cleaned, errs)
	cleaned, errs = cleanerupper.CleanDisks(c, *project, policy, dryRun)
	report("disks", cleaned, errs)
	cleaned, errs = cleanerupper.CleanLoadBalancerResources(c, *project, policy, regions, dryRun)
	report("loadbalancer", cleaned, errs)
	cleaned, errs = cleanerupper.CleanNetworks(c, *project, policy, dryRun)
	report("networks", cleaned, errs)
}
