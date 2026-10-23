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

package ootgve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDNFKernelExcludes(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name: "required excludes",
			config: `[main]
gpgcheck=1
exclude=kernel,kernel-*
`,
		},
		{
			name: "space separated and reordered",
			config: `[main]
exclude=kernel-* kernel vim
`,
		},
		{
			name: "missing kernel glob",
			config: `[main]
exclude=kernel
`,
			wantErr: "does not exclude both kernel and kernel-* packages",
		},
		{
			name: "exclude outside main section",
			config: `[main]
gpgcheck=1
[repository]
exclude=kernel,kernel-*
`,
			wantErr: "does not exclude both kernel and kernel-* packages",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDNFKernelExcludes([]byte(tc.config))
			if tc.wantErr == "" && err != nil {
				t.Fatalf("ValidateDNFKernelExcludes() = %v, want nil", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("ValidateDNFKernelExcludes() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestCheckDNFKernelExcludes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dnf.conf")
	if err := os.WriteFile(path, []byte("[main]\nexclude=kernel kernel-*\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CheckDNFKernelExcludes(path); err != nil {
		t.Fatalf("CheckDNFKernelExcludes(%q) = %v, want nil", path, err)
	}
}

func TestCheckDNFKernelExcludesReportsReadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dnf.conf")
	if err := CheckDNFKernelExcludes(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("CheckDNFKernelExcludes(%q) = %v, want error naming path", path, err)
	}
}

func TestValidateModuleInfo(t *testing.T) {
	tests := []struct {
		name       string
		taint      string
		modulePath string
		wantErr    string
	}{
		{name: "out of tree module", taint: "O", modulePath: "/lib/modules/6.12.0/extra/gve.ko"},
		{name: "out of tree and unsigned module", taint: "OE", modulePath: "/lib/modules/6.12.0/extra/gve.ko.xz"},
		{name: "kmod subdirectory", taint: "O", modulePath: "/lib/modules/6.12.0/extra/gve-el10_2/gve.ko"},
		{name: "weak updates kmod", taint: "O", modulePath: "/lib/modules/6.12.0/weak-updates/gve-el10_2/gve.ko"},
		{name: "misleading filename", taint: "O", modulePath: "/lib/modules/6.12.0/extra/gve.ko.backup", wantErr: "want the out-of-tree module"},
		{name: "unsigned in tree module", taint: "E", modulePath: "/lib/modules/6.12.0/extra/gve.ko", wantErr: "want out-of-tree flag O"},
		{name: "in tree path", taint: "O", modulePath: "/lib/modules/6.12.0/kernel/drivers/net/ethernet/google/gve/gve.ko.xz", wantErr: "want the out-of-tree module under /extra"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModuleInfo(tc.taint, tc.modulePath)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("ValidateModuleInfo() = %v, want nil", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("ValidateModuleInfo() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestCheckModule(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "modprobe":
			return nil, nil
		case "modinfo":
			return []byte("/lib/modules/6.12.0/extra/gve.ko.xz\n"), nil
		case "readlink":
			return []byte("/lib/modules/6.12.0/extra/gve.ko.xz\n"), nil
		case "rpm":
			return []byte("gve\n"), nil
		default:
			return nil, errors.New("unexpected command")
		}
	}
	readFile := func(path string) ([]byte, error) {
		if path != "/sys/module/gve/taint" {
			return nil, errors.New("unexpected path")
		}
		return []byte("OE\n"), nil
	}
	stat := func(path string) error {
		if path != "/sys/module/gve" {
			return errors.New("unexpected path")
		}
		return nil
	}

	if err := checkModule(context.Background(), run, readFile, stat); err != nil {
		t.Fatalf("checkModule() = %v, want nil", err)
	}
}

func TestCheckKmodModule(t *testing.T) {
	const selected = "/lib/modules/6.12.0-ciq/weak-updates/gve-el10_2/gve.ko"
	const target = "/usr/lib/modules/6.12.0/extra/gve-el10_2/gve.ko"
	for _, tc := range []struct {
		name, resolved, provider, owner, diskVersion, loadedVersion, wantErr string
		broken                                                               bool
	}{
		{name: "kmod", resolved: target, provider: "kmod-gve-el10_2", owner: "kmod-gve-el10_2", diskVersion: "1.4.10~26-oot", loadedVersion: "1.4.10~26-oot"},
		{name: "wrong owner", resolved: target, provider: "kmod-gve-el10_2", owner: "kernel-modules", wantErr: "owned by"},
		{name: "unexpected provider", resolved: target, provider: "unrelated", wantErr: "provider"},
		{name: "in tree target", resolved: "/lib/modules/6.12.0/kernel/drivers/net/ethernet/google/gve/gve.ko", provider: "kmod-gve-el10_2", wantErr: "out-of-tree module"},
		{name: "broken symlink", broken: true, wantErr: "resolving"},
		{name: "not oot version", resolved: target, provider: "kmod-gve-el10_2", owner: "kmod-gve-el10_2", diskVersion: "1.4.10", loadedVersion: "1.4.10", wantErr: "-oot"},
		{name: "stale loaded module", resolved: target, provider: "kmod-gve-el10_2", owner: "kmod-gve-el10_2", diskVersion: "1.4.10~26-oot", loadedVersion: "1.4.10~22-oot", wantErr: "loaded gve version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(_ context.Context, name string, args ...string) ([]byte, error) {
				switch name + " " + strings.Join(args, " ") {
				case "modprobe gve":
					return nil, nil
				case "modinfo -F filename gve":
					return []byte(selected), nil
				case "readlink -e " + selected:
					if tc.broken {
						return nil, errors.New("missing target")
					}
					return []byte(tc.resolved), nil
				case "rpm -q --qf %{NAME}\\n --whatprovides gve":
					return []byte(tc.provider + "\n"), nil
				case "rpm -qf --qf %{NAME}\\n " + tc.resolved:
					return []byte(tc.owner + "\n"), nil
				case "modinfo -F version " + tc.resolved:
					return []byte(tc.diskVersion + "\n"), nil
				default:
					t.Fatalf("unexpected command: %s %q", name, args)
					return nil, nil
				}
			}
			read := func(path string) ([]byte, error) {
				switch path {
				case "/sys/module/gve/taint":
					return []byte("OE\n"), nil
				case "/sys/module/gve/version":
					return []byte(tc.loadedVersion + "\n"), nil
				default:
					t.Fatalf("unexpected read: %s", path)
					return nil, nil
				}
			}
			err := checkModule(context.Background(), run, read, func(string) error { return nil })
			if tc.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestCheckModuleReportsModprobeError(t *testing.T) {
	run := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("module not found"), errors.New("exit status 1")
	}

	err := checkModule(context.Background(), run, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "modprobe gve failed") || !strings.Contains(err.Error(), "module not found") {
		t.Fatalf("checkModule() = %v, want modprobe error with command output", err)
	}
}
