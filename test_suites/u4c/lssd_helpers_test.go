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
	"regexp"
	"strconv"
	"strings"
)

// checkLocalSSDVisible enumerates whole kernel devices directly. U4C exposes
// Local SSDs as nvme_card and Hyperdisk as nvme_card-pd; guest by-id aliases
// need not provide a separate google-local-nvme-ssd-N link for every device.
func checkLocalSSDVisible(sysBlock string) error {
	entries, err := os.ReadDir(sysBlock)
	if err != nil {
		return fmt.Errorf("reading kernel block devices: %w", err)
	}
	wholeNVMe := regexp.MustCompile(`^nvme[0-9]+n[0-9]+$`)
	var seen []string
	var inventory []string
	for _, entry := range entries {
		name := entry.Name()
		if !wholeNVMe.MatchString(name) {
			continue
		}
		dir := filepath.Join(sysBlock, name)
		model, err := os.ReadFile(filepath.Join(dir, "device", "model"))
		if err != nil {
			return fmt.Errorf("reading %s model: %w", name, err)
		}
		identity := strings.TrimSpace(string(model))
		inventory = append(inventory, fmt.Sprintf("%s=%q", name, identity))
		// Exact identity matching excludes nvme_card-pd, including the boot disk.
		if identity != "nvme_card" && identity != "Google Local SSD" {
			continue
		}
		seen = append(seen, name)

		// /sys/block contains whole devices, and size is always in 512-byte sectors,
		// regardless of the device's logical block size.
		size, err := os.ReadFile(filepath.Join(dir, "size"))
		if err != nil {
			return fmt.Errorf("reading %s size: %w", name, err)
		}
		sectors, err := strconv.ParseUint(strings.TrimSpace(string(size)), 10, 64)
		const wantSectors = uint64(3000) * 1024 * 1024 * 1024 / 512
		if err != nil || sectors != wantSectors {
			return fmt.Errorf("%s size is %q sectors, want %d", name, strings.TrimSpace(string(size)), wantSectors)
		}
		ro, err := os.ReadFile(filepath.Join(dir, "ro"))
		if err != nil {
			return fmt.Errorf("reading %s read-only flag: %w", name, err)
		}
		if strings.TrimSpace(string(ro)) != "0" {
			return fmt.Errorf("%s read-only flag is %q, want 0", name, strings.TrimSpace(string(ro)))
		}
		// Namespace -> controller -> PCI device -> bound driver.
		driver, err := os.Readlink(filepath.Join(dir, "device", "device", "driver"))
		if err != nil {
			return fmt.Errorf("reading %s NVMe driver: %w", name, err)
		}
		if filepath.Base(driver) != "nvme" {
			return fmt.Errorf("%s driver is %q, want nvme", name, driver)
		}
	}
	if len(seen) != 4 {
		return fmt.Errorf("got %d distinct Local SSD devices (NVMe models: %v), want 4", len(seen), inventory)
	}
	return nil
}
