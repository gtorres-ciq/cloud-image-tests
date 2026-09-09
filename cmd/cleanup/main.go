// Command cleanup deletes leftover cloud-image-tests resources (instances,
// disks, load-balancer resources, networks) left behind by interrupted or
// failed test runs, using the cleanerupper library (which has no CLI of its
// own).
//
// It DRY-RUNS by default (only lists what it would delete). Pass -no-dry-run to
// actually delete. Only resources created more than -older-than ago are
// considered, so in-flight tests are safe; the default network,
// deletion-protected instances, and resources labeled do-not-delete are always
// kept (see cleanerupper.AgePolicy).
//
// Auth uses Application Default Credentials:
//
//	gcloud auth application-default login   # once
//	go run ./cmd/cleanup -project ciq-test-servers                # dry-run (list only)
//	go run ./cmd/cleanup -project ciq-test-servers -no-dry-run    # actually delete
package main

import (
	"context"
	"flag"
	"log"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/cloud-image-tests/cleanerupper"
)

func main() {
	project := flag.String("project", "", "GCP project to clean (required)")
	olderThan := flag.Duration("older-than", 2*time.Hour, "only touch resources created more than this ago; keep it larger than your longest in-flight test so running VMs are spared")
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
	policy := cleanerupper.AgePolicy(cutoff)
	regions := strings.Split(*regionsCSV, ",")

	verb := "deleted"
	if dryRun {
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
