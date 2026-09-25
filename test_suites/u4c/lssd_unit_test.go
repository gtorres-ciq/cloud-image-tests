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
	"strings"
	"testing"
)

func TestCheckLocalSSDVisible(t *testing.T) {
	for _, tc := range []struct{ name, change, want string }{
		{"U4C four devices but only one local SSD alias", "", ""},
		{"missing disk", "missing", "want 4"},
		{"wrong capacity", "size", "sectors"},
		{"read only", "ro", "read-only"},
		{"wrong driver", "driver", "driver"},
		{"missing size", "no-size", "size"},
		{"unknown model", "model", "want 4"},
		{"missing model", "no-model", "model"},
		{"standard local SSD model", "standard-model", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			ids, sys := filepath.Join(root, "by-id"), filepath.Join(root, "block")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(os.MkdirAll(ids, 0755))
			for i := 0; i < 5; i++ {
				name := fmt.Sprintf("nvme%dn1", i)
				dir := filepath.Join(sys, name)
				must(os.MkdirAll(filepath.Join(dir, "device", "device"), 0755))
				model, size := "nvme_card   \n", "6291456000\n"
				if i == 0 {
					model, size = "nvme_card-pd\n", "41943040\n"
				}
				must(os.WriteFile(filepath.Join(dir, "device", "model"), []byte(model), 0644))
				must(os.WriteFile(filepath.Join(dir, "size"), []byte(size), 0644))
				must(os.WriteFile(filepath.Join(dir, "ro"), []byte("0\n"), 0644))
				must(os.Symlink("/sys/bus/pci/drivers/nvme", filepath.Join(dir, "device", "device", "driver")))
				must(os.WriteFile(filepath.Join(root, name), nil, 0644))
			}
			must(os.Symlink(filepath.Join(root, "nvme2n1"), filepath.Join(ids, "google-local-nvme-ssd-0")))
			must(os.Symlink(filepath.Join(root, "nvme2n1"), filepath.Join(ids, "nvme-alias")))
			// Non-NVMe devices and partitions must not enter discovery.
			must(os.MkdirAll(filepath.Join(sys, "loop0"), 0755))
			must(os.MkdirAll(filepath.Join(sys, "nvme1n1p1"), 0755))
			dir := filepath.Join(sys, "nvme4n1")
			switch tc.change {
			case "missing":
				must(os.RemoveAll(dir))
			case "size":
				must(os.WriteFile(filepath.Join(dir, "size"), []byte("786432000"), 0644))
			case "ro":
				must(os.WriteFile(filepath.Join(dir, "ro"), []byte("1"), 0644))
			case "driver":
				must(os.Remove(filepath.Join(dir, "device", "device", "driver")))
				must(os.Symlink("/sys/bus/pci/drivers/other", filepath.Join(dir, "device", "device", "driver")))
			case "no-size":
				must(os.Remove(filepath.Join(dir, "size")))
			case "model":
				must(os.WriteFile(filepath.Join(dir, "device", "model"), []byte("unknown"), 0644))
			case "no-model":
				must(os.Remove(filepath.Join(dir, "device", "model")))
			case "standard-model":
				must(os.WriteFile(filepath.Join(dir, "device", "model"), []byte("Google Local SSD\n"), 0644))
			}
			err := checkLocalSSDVisible(sys)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}
