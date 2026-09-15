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

// Package ootgve contains reusable validation for Rocky Linux images that use
// the out-of-tree gVNIC driver.
package ootgve

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode"
)

type commandRunner func(context.Context, string, ...string) ([]byte, error)
type fileReader func(string) ([]byte, error)
type pathStatter func(string) error

// ValidateDNFKernelExcludes verifies that the DNF main configuration excludes
// both the kernel package and all kernel subpackages.
func ValidateDNFKernelExcludes(config []byte) error {
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
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanning DNF configuration: %w", err)
	}
	if !hasKernel || !hasKernelGlob {
		return fmt.Errorf("does not exclude both kernel and kernel-* packages")
	}
	return nil
}

// CheckDNFKernelExcludes reads and validates a DNF configuration file.
func CheckDNFKernelExcludes(path string) error {
	config, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %q: %w", path, err)
	}
	if err := ValidateDNFKernelExcludes(config); err != nil {
		return fmt.Errorf("%s %w; contents:\n%s", path, err, config)
	}
	return nil
}

// ValidateModuleInfo verifies that gve is marked out-of-tree and resolves to
// the driver installed under the kernel's extra module directory.
func ValidateModuleInfo(taint, modulePath string) error {
	if !strings.Contains(taint, "O") {
		return fmt.Errorf("loaded gve module taint is %q, want out-of-tree flag O", taint)
	}
	if !strings.Contains(modulePath, "/extra/gve.ko") {
		return fmt.Errorf("modinfo resolved gve to %q, want the out-of-tree module under /extra", modulePath)
	}
	return nil
}

func checkModule(ctx context.Context, run commandRunner, readFile fileReader, stat pathStatter) error {
	if output, err := run(ctx, "modprobe", "gve"); err != nil {
		return fmt.Errorf("modprobe gve failed: %w; output: %s", err, output)
	}
	if err := stat("/sys/module/gve"); err != nil {
		return fmt.Errorf("gve is not loaded after modprobe: %w", err)
	}
	taint, err := readFile("/sys/module/gve/taint")
	if err != nil {
		return fmt.Errorf("reading loaded gve module taint failed: %w", err)
	}
	output, err := run(ctx, "modinfo", "-F", "filename", "gve")
	if err != nil {
		return fmt.Errorf("modinfo gve failed: %w; output: %s", err, output)
	}
	return ValidateModuleInfo(strings.TrimSpace(string(taint)), strings.TrimSpace(string(output)))
}

// CheckModule loads gve and verifies that the active module is the out-of-tree
// driver installed by the image.
func CheckModule(ctx context.Context) error {
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	stat := func(path string) error {
		_, err := os.Stat(path)
		return err
	}
	return checkModule(ctx, run, os.ReadFile, stat)
}
