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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type commandRunner func(context.Context, string, ...string) ([]byte, error)
type fileReader func(string) ([]byte, error)
type pathStatter func(string) error

var modulePathPattern = regexp.MustCompile(`^/(usr/)?lib/modules/[^/]+/(extra|weak-updates)/(.+/)?gve\.ko(\.(xz|gz|zst))?$`)

// ValidateModuleInfo verifies the loaded out-of-tree flag and a supported
// module lookup path. CheckModule also resolves symlinks and validates kmod
// ownership and the loaded version.
func ValidateModuleInfo(taint, modulePath string) error {
	if !strings.Contains(taint, "O") {
		return fmt.Errorf("loaded gve module taint is %q, want out-of-tree flag O", taint)
	}
	if !modulePathPattern.MatchString(filepath.Clean(modulePath)) {
		return fmt.Errorf("modinfo resolved gve to %q, want the out-of-tree module under /extra or /weak-updates", modulePath)
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
	modulePath := strings.TrimSpace(string(output))
	if err := ValidateModuleInfo(strings.TrimSpace(string(taint)), modulePath); err != nil {
		return err
	}
	output, err = run(ctx, "readlink", "-e", modulePath)
	if err != nil {
		return fmt.Errorf("resolving gve module %q failed: %w; output: %s", modulePath, err, output)
	}
	resolved := strings.TrimSpace(string(output))
	if err := ValidateModuleInfo(string(taint), resolved); err != nil {
		return err
	}
	if !strings.Contains(filepath.Clean(resolved), "/extra/") {
		return fmt.Errorf("gve module target %q must be under /extra", resolved)
	}
	output, err = run(ctx, "rpm", "-q", "--qf", "%{NAME}\\n", "--whatprovides", "gve")
	if err != nil {
		return fmt.Errorf("querying installed gve provider failed: %w; output: %s", err, output)
	}
	providers := strings.Fields(string(output))
	if len(providers) != 1 {
		return fmt.Errorf("expected one installed gve provider, got %q", output)
	}
	provider := providers[0]
	// Older images use DKMS: the generated module is not owned by the RPM.
	if provider == "gve" {
		return nil
	}
	if provider != "kmod-gve" && !strings.HasPrefix(provider, "kmod-gve-") {
		return fmt.Errorf("unexpected gve provider %q, want gve or kmod-gve package", provider)
	}
	output, err = run(ctx, "rpm", "-qf", "--qf", "%{NAME}\\n", resolved)
	if err != nil {
		return fmt.Errorf("querying gve module owner failed: %w; output: %s", err, output)
	}
	if owner := strings.TrimSpace(string(output)); owner != provider {
		return fmt.Errorf("gve module %q is owned by %q, want installed provider %q", resolved, owner, provider)
	}
	output, err = run(ctx, "modinfo", "-F", "version", resolved)
	if err != nil {
		return fmt.Errorf("reading gve module version failed: %w; output: %s", err, output)
	}
	version := strings.TrimSpace(string(output))
	if !strings.HasSuffix(version, "-oot") {
		return fmt.Errorf("gve kmod version %q does not have the -oot suffix", version)
	}
	loaded, err := readFile("/sys/module/gve/version")
	if err != nil {
		return fmt.Errorf("reading loaded gve version failed: %w", err)
	}
	if loadedVersion := strings.TrimSpace(string(loaded)); loadedVersion != version {
		return fmt.Errorf("loaded gve version %q does not match installed kmod version %q", loadedVersion, version)
	}
	return nil
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
