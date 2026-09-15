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

// Package u4c smoke tests Rocky Linux OOT-GVE images on U4C bare-metal VMs.
package u4c

import (
	"fmt"
	"strings"

	imagetest "github.com/GoogleCloudPlatform/cloud-image-tests"
	daisy "github.com/GoogleCloudPlatform/compute-daisy"
	"google.golang.org/api/compute/v1"
)

// Name is the name of the test package. It must match the directory name.
var Name = "u4c"

const (
	rockyOOTGVEFamily  = "rocky-linux-10-optimized-gcp-oot-gve"
	generalNetworkName = "u4c-general"
	generalSubnetName  = "u4c-general-subnet"
	ullNetworkName     = "u4c-ull"
	ullSubnetOneName   = "u4c-ull-subnet-1"
	ullSubnetTwoName   = "u4c-ull-subnet-2"
)

type networkPlan struct {
	generalNetwork *daisy.Network
	ullNetwork     *daisy.Network
	subnetworks    []*daisy.Subnetwork
	interfaces     []*compute.NetworkInterface
}

func regionFromZone(zone string) (string, error) {
	i := strings.LastIndex(zone, "-")
	if strings.Count(zone, "-") < 2 || i <= 0 || i == len(zone)-1 {
		return "", fmt.Errorf("invalid zone %q", zone)
	}
	return zone[:i], nil
}

func buildNetworkPlan(project, zone string) (*networkPlan, error) {
	region, err := regionFromZone(zone)
	if err != nil {
		return nil, err
	}
	falseValue := false
	generalNetwork := &daisy.Network{
		Network:               compute.Network{Name: generalNetworkName},
		AutoCreateSubnetworks: &falseValue,
	}
	ullNetwork := &daisy.Network{
		Network: compute.Network{
			Name:           ullNetworkName,
			NetworkProfile: fmt.Sprintf("projects/%s/global/networkProfiles/%s-vpc-ull-participant", project, zone),
		},
		AutoCreateSubnetworks: &falseValue,
	}
	subnetworks := []*daisy.Subnetwork{
		{Subnetwork: compute.Subnetwork{Name: generalSubnetName, Network: generalNetworkName, Region: region, IpCidrRange: "10.128.0.0/20", StackType: "IPV4_ONLY"}},
		{Subnetwork: compute.Subnetwork{Name: ullSubnetOneName, Network: ullNetworkName, Region: region, IpCidrRange: "10.129.0.0/24", StackType: "IPV4_ONLY"}},
		{Subnetwork: compute.Subnetwork{Name: ullSubnetTwoName, Network: ullNetworkName, Region: region, IpCidrRange: "10.130.0.0/24", StackType: "IPV4_ONLY"}},
	}
	interfaces := []*compute.NetworkInterface{
		{
			NicType:    "GVNIC",
			Network:    generalNetworkName,
			Subnetwork: generalSubnetName,
			QueueCount: 32,
			AccessConfigs: []*compute.AccessConfig{{
				Name: "External NAT",
				Type: "ONE_TO_ONE_NAT",
			}},
		},
		{NicType: "GVNIC", Network: ullNetworkName, Subnetwork: ullSubnetOneName, QueueCount: 32, AccessConfigs: []*compute.AccessConfig{}},
		{NicType: "GVNIC", Network: ullNetworkName, Subnetwork: ullSubnetTwoName, QueueCount: 32, AccessConfigs: []*compute.AccessConfig{}},
	}
	return &networkPlan{generalNetwork: generalNetwork, ullNetwork: ullNetwork, subnetworks: subnetworks, interfaces: interfaces}, nil
}

func isRockyOOTGVEImage(name, family string) bool {
	return family == rockyOOTGVEFamily || name == rockyOOTGVEFamily || strings.HasPrefix(name, rockyOOTGVEFamily+"-v")
}

// TestSetup creates the two VPCs and three NICs required by U4C, then boots one
// VM for the gVNIC visibility smoke test.
func TestSetup(t *imagetest.TestWorkflow) error {
	if !strings.HasPrefix(t.MachineType.Name, "u4c-") {
		return fmt.Errorf("u4c suite requires a U4C machine type, got %q", t.MachineType.Name)
	}
	if !isRockyOOTGVEImage(t.Image.Name, t.Image.Family) {
		t.Skip("The U4C smoke suite only tests Rocky Linux OOT-GVE images")
		return nil
	}

	plan, err := buildNetworkPlan(t.Project.Name, t.Zone.Name)
	if err != nil {
		return err
	}
	generalNetwork, err := t.CreateNetworkFromDaisyNetwork(plan.generalNetwork)
	if err != nil {
		return fmt.Errorf("creating general-purpose U4C network: %w", err)
	}
	ullNetwork, err := t.CreateNetworkFromDaisyNetwork(plan.ullNetwork)
	if err != nil {
		return fmt.Errorf("creating participant ULL network: %w", err)
	}
	if _, err := generalNetwork.CreateSubnetworkFromDaisySubnetwork(plan.subnetworks[0]); err != nil {
		return fmt.Errorf("creating general-purpose U4C subnet: %w", err)
	}
	for _, subnet := range plan.subnetworks[1:] {
		if _, err := ullNetwork.CreateSubnetworkFromDaisySubnetwork(subnet); err != nil {
			return fmt.Errorf("creating participant ULL subnet %q: %w", subnet.Name, err)
		}
	}

	instance := &daisy.Instance{
		Instance: compute.Instance{
			Zone:              t.Zone.Name,
			MachineType:       t.MachineType.Name,
			NetworkInterfaces: plan.interfaces,
			Scheduling:        &compute.Scheduling{OnHostMaintenance: "TERMINATE"},
		},
	}
	bootDisk := &compute.Disk{
		Name: "u4csmoke",
		Type: imagetest.HyperdiskBalanced,
		Zone: t.Zone.Name,
	}
	vm, err := t.CreateTestVMMultipleDisks([]*compute.Disk{bootDisk}, instance)
	if err != nil {
		return err
	}
	vm.RunTests("TestGVNICsVisible")
	return nil
}
