package main

import (
	"reflect"
	"testing"
	"time"

	"google.golang.org/api/compute/v1"
)

func TestCleanupPolicyWorkflowIDs(t *testing.T) {
	policy, ids, err := cleanupPolicy(" 9vxsp,bdg12,9vxsp ", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"9vxsp", "bdg12"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("IDs = %v, want %v", ids, want)
	}
	for _, tc := range []struct {
		name string
		vm   *compute.Instance
		want bool
	}{
		{"first workflow", &compute.Instance{Name: "boot-9vxsp"}, true},
		{"second workflow", &compute.Instance{Name: "boot-bdg12"}, true},
		{"unselected workflow", &compute.Instance{Name: "boot-zzzzz"}, false},
		{"protected", &compute.Instance{Name: "boot-9vxsp", DeletionProtection: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := policy(tc.vm); got != tc.want {
				t.Fatalf("policy() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCleanupPolicyRejectsMalformedWorkflowIDs(t *testing.T) {
	for _, value := range []string{"short", "ABC12", "9vxsp,bad!"} {
		t.Run(value, func(t *testing.T) {
			if _, _, err := cleanupPolicy(value, time.Now()); err == nil {
				t.Fatal("cleanupPolicy accepted malformed workflow ID")
			}
		})
	}
}
