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
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/cloud-image-tests/utils"
)

// TestLocalSSDVisible checks the four 3,000 GiB Titanium SSDs included with
// U4C lssd shapes. It only inspects devices; it does not format or write to them.
func TestLocalSSDVisible(t *testing.T) {
	utils.LinuxOnly(t)
	ctx, cancel := context.WithTimeout(utils.Context(t), 30*time.Second)
	defer cancel()
	machineType, err := utils.GetMetadata(ctx, "instance", "machine-type")
	if err != nil {
		t.Fatalf("reading machine type: %v", err)
	}
	shape := filepath.Base(strings.TrimSpace(machineType))
	switch shape {
	case "u4c-standard-120-metal", "u4c-highcpu-120-metal":
		t.Skipf("%s has no Local SSDs", shape)
	case "u4c-standard-120-lssd-metal", "u4c-highcpu-120-lssd-metal":
	default:
		t.Fatalf("unexpected machine type %q for U4C Local SSD check", shape)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		err = checkLocalSSDVisible("/sys/block")
		if err == nil {
			t.Log("All four 3,000 GiB Local SSDs are visible, writable and bound to nvme")
			return
		}
		select {
		case <-ctx.Done():
			diagnosticCtx, diagnosticCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer diagnosticCancel()
			out, diagnosticErr := exec.CommandContext(diagnosticCtx, "lsblk", "-b", "-o", "NAME,TYPE,SIZE,RO,MODEL,MOUNTPOINT").CombinedOutput()
			t.Logf("lsblk inventory (error: %v):\n%s", diagnosticErr, out)
			t.Fatalf("Local SSD discovery did not succeed before deadline: %v (last check: %v)", ctx.Err(), err)
		case <-ticker.C:
		}
	}
}
