// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package u4c

import "testing"

func TestIsRockyOOTGVEImage(t *testing.T) {
	tests := []struct {
		name, image, family string
		want                bool
	}{
		{name: "family image", image: "rocky-linux-10-optimized-gcp-oot-gve-v20260911", family: "rocky-linux-10-optimized-gcp-oot-gve", want: true},
		{name: "custom lookalike", image: "custom-rocky-linux-10-optimized-gcp-oot-gve-v20260911", want: false},
		{name: "Rocky 9 OOT", image: "rocky-linux-9-optimized-gcp-oot-gve-v20260911", family: "rocky-linux-9-optimized-gcp-oot-gve", want: false},
		{name: "Rocky without OOT", image: "rocky-linux-10-optimized-gcp-v20260911", family: "rocky-linux-10-optimized-gcp", want: false},
		{name: "non-Rocky OOT", image: "rhel-10-2-oot-gve-v20260911", family: "rhel-10-2-oot-gve", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRockyOOTGVEImage(tc.image, tc.family); got != tc.want {
				t.Errorf("isRockyOOTGVEImage(%q, %q) = %t, want %t", tc.image, tc.family, got, tc.want)
			}
		})
	}
}

func TestBuildNetworkPlanCreatesRequiredU4CTopology(t *testing.T) {
	plan, err := buildNetworkPlan("test-project", "us-south1-d")
	if err != nil {
		t.Fatalf("buildNetworkPlan() = err %v, want nil", err)
	}

	if plan.generalNetwork.NetworkProfile != "" {
		t.Errorf("general network profile = %q, want regular VPC", plan.generalNetwork.NetworkProfile)
	}
	if got, want := plan.ullNetwork.NetworkProfile, "projects/test-project/global/networkProfiles/us-south1-d-vpc-ull-participant"; got != want {
		t.Errorf("ULL network profile = %q, want %q", got, want)
	}
	if got := len(plan.subnetworks); got != 3 {
		t.Fatalf("subnetwork count = %d, want 3", got)
	}
	if got := len(plan.interfaces); got != 3 {
		t.Fatalf("interface count = %d, want 3", got)
	}
	for i, nic := range plan.interfaces {
		if nic.NicType != "GVNIC" {
			t.Errorf("nic%d type = %q, want GVNIC", i, nic.NicType)
		}
		if nic.QueueCount != 32 {
			t.Errorf("nic%d queue count = %d, want 32", i, nic.QueueCount)
		}
	}
	if got := len(plan.interfaces[0].AccessConfigs); got != 1 {
		t.Errorf("nic0 access config count = %d, want 1", got)
	}
	for i := 1; i < 3; i++ {
		if got := len(plan.interfaces[i].AccessConfigs); got != 0 {
			t.Errorf("nic%d access config count = %d, want 0", i, got)
		}
	}
	if plan.interfaces[1].Network != plan.interfaces[2].Network {
		t.Errorf("nic1 and nic2 must use the same ULL VPC: %q != %q", plan.interfaces[1].Network, plan.interfaces[2].Network)
	}
	if plan.interfaces[1].Subnetwork == plan.interfaces[2].Subnetwork {
		t.Errorf("nic1 and nic2 must use distinct ULL subnets: %q", plan.interfaces[1].Subnetwork)
	}
}

func TestBuildNetworkPlanRejectsInvalidZone(t *testing.T) {
	if _, err := buildNetworkPlan("test-project", "us-south1"); err == nil {
		t.Fatal("buildNetworkPlan() = nil error, want invalid-zone error")
	}
}
