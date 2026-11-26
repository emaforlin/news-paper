package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func entries(t *testing.T, dir string) string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return strings.Join(names, " ")
}

func TestFileStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := NewFileStore(path)

	s, err := store.Load(ctx)
	if err != nil || len(s.Seen) != 0 {
		t.Fatalf("missing file: %v, %v", s, err)
	}
	s.Add(now, "a")
	if err := store.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := entries(t, dir); got != "state.json" {
		t.Errorf("dir = %q", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, %v", fi.Mode(), err)
	}
	if got, err := store.Load(ctx); err != nil || !got.Has("a") {
		t.Errorf("round trip: %v, %v", got, err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Save(cancelled, New()); err == nil {
		t.Error("save with a cancelled context")
	}
	if _, err := store.Load(cancelled); err == nil {
		t.Error("load with a cancelled context")
	}
	if got, _ := store.Load(ctx); !got.Has("a") {
		t.Error("cancelled save changed the file")
	}
}

func TestFileStoreErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	asDir := filepath.Join(dir, "isdir")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{corrupt, asDir} {
		if _, err := NewFileStore(path).Load(ctx); err == nil || !strings.Contains(err.Error(), "state load "+path) {
			t.Errorf("Load(%s) = %v", path, err)
		}
	}
	for _, path := range []string{asDir, filepath.Join(dir, "nope", "s.json")} {
		if err := NewFileStore(path).Save(ctx, New()); err == nil || !strings.Contains(err.Error(), "state save "+path) {
			t.Errorf("Save(%s) = %v", path, err)
		}
	}
	if got := entries(t, dir); got != "corrupt.json isdir" {
		t.Errorf("temp files left: %q", got)
	}
}
