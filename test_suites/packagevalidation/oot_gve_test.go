// Copyright 2026 Google LLC.
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

package packagevalidation

import (
	"testing"

	"github.com/GoogleCloudPlatform/cloud-image-tests/utils"
	"github.com/GoogleCloudPlatform/cloud-image-tests/utils/ootgve"
)

const dnfConfigPath = "/etc/dnf/dnf.conf"

func TestOOTGVEDNFExcludes(t *testing.T) {
	utils.LinuxOnly(t)

	if err := ootgve.CheckDNFKernelExcludes(dnfConfigPath); err != nil {
		t.Fatal(err)
	}
}

func TestOOTGVEModule(t *testing.T) {
	utils.LinuxOnly(t)
	if err := ootgve.CheckModule(utils.Context(t)); err != nil {
		t.Fatal(err)
	}
}
