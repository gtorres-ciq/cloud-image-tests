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
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode"

	"github.com/GoogleCloudPlatform/cloud-image-tests/utils"
)

const dnfConfigPath = "/etc/dnf/dnf.conf"

func isOOTModuleTaint(taint string) bool {
	return strings.Contains(taint, "O")
}

func hasDNFKernelExcludes(config []byte) bool {
	inMain := false
	hasKernel := false
	hasKernelGlob := false

	scanner := bufio.NewScanner(bytes.NewReader(config))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inMain = strings.EqualFold(line, "[main]")
			continue
		}
		if !inMain || strings.HasPrefix(line, "#") {
			continue
		}

		keyValue := strings.SplitN(line, "=", 2)
		if len(keyValue) != 2 || !strings.EqualFold(strings.TrimSpace(keyValue[0]), "exclude") {
			continue
		}

		hasKernel = false
		hasKernelGlob = false
		for _, exclude := range strings.FieldsFunc(keyValue[1], func(r rune) bool {
			return r == ',' || unicode.IsSpace(r)
		}) {
			switch exclude {
			case "kernel":
				hasKernel = true
			case "kernel-*":
				hasKernelGlob = true
			}
		}
	}

	return hasKernel && hasKernelGlob
}

func TestOOTGVEDNFExcludes(t *testing.T) {
	utils.LinuxOnly(t)

	config, err := os.ReadFile(dnfConfigPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) failed: %v", dnfConfigPath, err)
	}
	if !hasDNFKernelExcludes(config) {
		t.Fatalf("%s does not exclude both kernel and kernel-* packages; contents:\n%s", dnfConfigPath, config)
	}
}

func TestOOTGVEModule(t *testing.T) {
	utils.LinuxOnly(t)
	ctx := utils.Context(t)

	if output, err := exec.CommandContext(ctx, "modprobe", "gve").CombinedOutput(); err != nil {
		t.Fatalf("modprobe gve failed: %v; output: %s", err, output)
	}
	if _, err := os.Stat("/sys/module/gve"); err != nil {
		t.Fatalf("gve is not loaded after modprobe: %v", err)
	}
	taint, err := os.ReadFile("/sys/module/gve/taint")
	if err != nil {
		t.Fatalf("reading loaded gve module taint failed: %v", err)
	}
	if !isOOTModuleTaint(strings.TrimSpace(string(taint))) {
		t.Fatalf("loaded gve module taint is %q, want out-of-tree flag O", strings.TrimSpace(string(taint)))
	}

	output, err := exec.CommandContext(ctx, "modinfo", "-F", "filename", "gve").CombinedOutput()
	if err != nil {
		t.Fatalf("modinfo gve failed: %v; output: %s", err, output)
	}
	modulePath := strings.TrimSpace(string(output))
	if !strings.Contains(modulePath, "/extra/gve.ko") {
		t.Fatalf("modinfo resolved gve to %q, want the out-of-tree module under /extra", modulePath)
	}
}
