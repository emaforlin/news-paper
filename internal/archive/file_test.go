package archive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const date = "2026-11-26"

func TestFilePutIfAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "editions", "nested")
	a := NewFile(dir)
	ctx := context.Background()

	for i, data := range []string{"first", "second"} {
		created, err := a.PutIfAbsent(ctx, date, []byte(data))
		if err != nil || created != (i == 0) {
			t.Fatalf("put %d: %v, %v", i, created, err)
		}
		got, err := os.ReadFile(filepath.Join(dir, date+".epub"))
		if err != nil || string(got) != "first" {
			t.Errorf("content = %q, %v", got, err)
		}
		if es, _ := os.ReadDir(dir); len(es) != 1 {
			t.Errorf("entries after put %d: %v", i, es)
		}
	}
}

func TestFileConcurrent(t *testing.T) {
	a := NewFile(t.TempDir())
	var created atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := a.PutIfAbsent(context.Background(), date, []byte("x"))
			if err != nil {
				t.Error(err)
			}
			if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Errorf("created %d times", created.Load())
	}
}

func TestFileErrors(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	readonly := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(readonly, 0o500); err != nil {
		t.Fatal(err)
	}
	dirs := []string{file}
	if os.Geteuid() != 0 { // root ignores the permission bits
		dirs = append(dirs, readonly)
	}
	for _, dir := range dirs {
		created, err := NewFile(dir).PutIfAbsent(ctx, date, nil)
		if created || err == nil || !strings.Contains(err.Error(), "archive "+filepath.Join(dir, date+".epub")) {
			t.Errorf("%s: %v, %v", dir, created, err)
		}
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	dir := filepath.Join(t.TempDir(), "never")
	if _, err := NewFile(dir).PutIfAbsent(cancelled, date, nil); err == nil {
		t.Error("cancelled context accepted")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("cancelled put created the directory")
	}
}
