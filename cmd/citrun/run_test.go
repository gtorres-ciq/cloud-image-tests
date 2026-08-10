// cmd/citrun/run_test.go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunDirFresh(t *testing.T) {
	dir := t.TempDir()
	if err := runDirFresh(dir); err != nil {
		t.Fatalf("runDirFresh: want nil for a dir with no state.json, got %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runDirFresh(dir); err == nil {
		t.Fatal("runDirFresh: want error once state.json exists, got nil")
	}
}
