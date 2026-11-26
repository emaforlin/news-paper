package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInvalidDate(t *testing.T) {
	dir := t.TempDir()
	for _, a := range []Archive{NewFile(filepath.Join(dir, "ed")), NewNoop()} {
		for _, date := range []string{"2026-13-01", "2026-1-2", "../x", "", "2026-02-30"} {
			if created, err := a.PutIfAbsent(context.Background(), date, []byte("x")); created || err == nil {
				t.Errorf("%T %q: %v, %v", a, date, created, err)
			}
		}
	}
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Errorf("something was written: %v", es)
	}
}

func TestNoop(t *testing.T) {
	a := NewNoop()
	for range 2 {
		if created, err := a.PutIfAbsent(context.Background(), "2026-11-26", nil); !created || err != nil {
			t.Errorf("%v, %v", created, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if created, err := a.PutIfAbsent(ctx, "2026-11-26", nil); created || err == nil {
		t.Errorf("cancelled: %v, %v", created, err)
	}
}
