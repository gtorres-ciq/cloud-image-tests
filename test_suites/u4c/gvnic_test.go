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

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/cloud-image-tests/utils"
	"github.com/GoogleCloudPlatform/cloud-image-tests/utils/networkutils"
	"github.com/GoogleCloudPlatform/cloud-image-tests/utils/ootgve"
)

type nicResolver func(mac string) (name, driver string, err error)

func validateGVNICBindings(interfaces []networkutils.NetworkInterface, resolve nicResolver) error {
	if len(interfaces) != 3 {
		return fmt.Errorf("got %d network interfaces, want 3", len(interfaces))
	}
	seenKernelInterfaces := make(map[string]bool)
	for i, iface := range interfaces {
		if iface.NICType != networkutils.NICTypeGVNIC {
			return fmt.Errorf("metadata nic%d type is %q, want GVNIC", i, iface.NICType)
		}
		name, driver, err := resolve(iface.MAC)
		if err != nil {
			return fmt.Errorf("resolving metadata nic%d MAC %s in the kernel: %w", i, iface.MAC, err)
		}
		if driver != "gve" {
			return fmt.Errorf("kernel interface %s for metadata nic%d MAC %s uses driver %q, want gve", name, i, iface.MAC, driver)
		}
		if seenKernelInterfaces[name] {
			return fmt.Errorf("duplicate kernel interface %q resolved for metadata nic%d MAC %s", name, i, iface.MAC)
		}
		seenKernelInterfaces[name] = true
	}
	return nil
}

func kernelNICForMAC(mac string) (string, string, error) {
	iface, err := utils.GetInterfaceByMAC(mac)
	if err != nil {
		return "", "", err
	}
	driverLink := fmt.Sprintf("/sys/class/net/%s/device/driver", iface.Name)
	driverPath, err := os.Readlink(driverLink)
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w", driverLink, err)
	}
	return iface.Name, filepath.Base(driverPath), nil
}

// TestGVNICsVisible verifies that all three U4C NICs advertised by Compute
// Engine are visible to Linux and bound to the gve device driver.
func TestGVNICsVisible(t *testing.T) {
	utils.LinuxOnly(t)
	interfaces, err := networkutils.ListMDSIfaces(utils.Context(t))
	if err != nil {
		t.Fatalf("listing network interfaces from metadata: %v", err)
	}
	resolve := func(mac string) (string, string, error) {
		name, driver, err := kernelNICForMAC(mac)
		if err == nil {
			t.Logf("metadata MAC %s maps to kernel interface %s using driver %s", mac, name, driver)
		}
		return name, driver, err
	}
	if err := validateGVNICBindings(interfaces, resolve); err != nil {
		t.Fatal(err)
	}
}

func TestOOTGVEDNFExcludes(t *testing.T) {
	utils.LinuxOnly(t)

	if err := ootgve.CheckDNFKernelExcludes("/etc/dnf/dnf.conf"); err != nil {
		t.Fatal(err)
	}
}

func TestOOTGVEModule(t *testing.T) {
	utils.LinuxOnly(t)

	if err := ootgve.CheckModule(utils.Context(t)); err != nil {
		t.Fatal(err)
	}
}
