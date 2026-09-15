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
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/cloud-image-tests/utils/networkutils"
)

func TestValidateGVNICBindingsAcceptsThreeGVEDevices(t *testing.T) {
	interfaces := []networkutils.NetworkInterface{
		{MAC: "00:00:00:00:00:01", NICType: "GVNIC"},
		{MAC: "00:00:00:00:00:02", NICType: "GVNIC"},
		{MAC: "00:00:00:00:00:03", NICType: "GVNIC"},
	}
	resolve := func(mac string) (string, string, error) {
		return "eth" + mac[len(mac)-1:], "gve", nil
	}

	if err := validateGVNICBindings(interfaces, resolve); err != nil {
		t.Fatalf("validateGVNICBindings() = %v, want nil", err)
	}
}

func TestValidateGVNICBindingsRejectsMissingKernelDevice(t *testing.T) {
	interfaces := []networkutils.NetworkInterface{
		{MAC: "00:00:00:00:00:01", NICType: "GVNIC"},
		{MAC: "00:00:00:00:00:02", NICType: "GVNIC"},
		{MAC: "00:00:00:00:00:03", NICType: "GVNIC"},
	}
	resolve := func(mac string) (string, string, error) {
		if strings.HasSuffix(mac, "02") {
			return "", "", fmt.Errorf("not found")
		}
		return "eth0", "gve", nil
	}

	if err := validateGVNICBindings(interfaces, resolve); err == nil || !strings.Contains(err.Error(), "00:00:00:00:00:02") {
		t.Fatalf("validateGVNICBindings() = %v, want error naming missing MAC", err)
	}
}

func TestValidateGVNICBindingsRejectsDuplicateKernelDevice(t *testing.T) {
	interfaces := []networkutils.NetworkInterface{
		{MAC: "00:00:00:00:00:01", NICType: "GVNIC"},
		{MAC: "00:00:00:00:00:02", NICType: "GVNIC"},
		{MAC: "00:00:00:00:00:03", NICType: "GVNIC"},
	}
	resolve := func(string) (string, string, error) { return "eth0", "gve", nil }

	if err := validateGVNICBindings(interfaces, resolve); err == nil || !strings.Contains(err.Error(), "duplicate kernel interface") {
		t.Fatalf("validateGVNICBindings() = %v, want duplicate-kernel-interface error", err)
	}
}

func TestValidateGVNICBindingsRejectsWrongCountTypeAndDriver(t *testing.T) {
	tests := []struct {
		name       string
		interfaces []networkutils.NetworkInterface
		driver     string
		want       string
	}{
		{
			name: "wrong count",
			interfaces: []networkutils.NetworkInterface{
				{MAC: "01", NICType: "GVNIC"}, {MAC: "02", NICType: "GVNIC"},
			},
			driver: "gve",
			want:   "got 2 network interfaces, want 3",
		},
		{
			name: "wrong type",
			interfaces: []networkutils.NetworkInterface{
				{MAC: "01", NICType: "GVNIC"}, {MAC: "02", NICType: "VIRTIO_NET"}, {MAC: "03", NICType: "GVNIC"},
			},
			driver: "gve",
			want:   "VIRTIO_NET",
		},
		{
			name: "wrong driver",
			interfaces: []networkutils.NetworkInterface{
				{MAC: "01", NICType: "GVNIC"}, {MAC: "02", NICType: "GVNIC"}, {MAC: "03", NICType: "GVNIC"},
			},
			driver: "virtio_net",
			want:   "virtio_net",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolve := func(string) (string, string, error) { return "eth0", tc.driver, nil }
			err := validateGVNICBindings(tc.interfaces, resolve)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateGVNICBindings() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}
