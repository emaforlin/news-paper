package state

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// fileStore keeps the state in a local JSON file. It has no locking: the CLI
// isn't run concurrently with itself.
type fileStore struct{ path string }

// NewFileStore returns a Store over the file at path. The parent directory
// must exist.
func NewFileStore(path string) Store { return &fileStore{path: path} }

// Load returns an empty state when the file doesn't exist.
func (f *fileStore) Load(ctx context.Context) (*State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("state load %s: %w", f.path, err)
	}
	s, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("state load %s: %w", f.path, err)
	}
	return s, nil
}

// Save replaces the file atomically.
func (f *fileStore) Save(ctx context.Context, s *State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeAtomic(f.path, s); err != nil {
		return fmt.Errorf("state save %s: %w", f.path, err)
	}
	return nil
}

// writeAtomic writes a temp file beside path, syncs it and renames it over.
func writeAtomic(path string, s *State) (err error) {
	data, err := encode(s)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()
	_, werr := tmp.Write(data)
	// Every step runs so the file always gets closed; any failure aborts.
	if err = errors.Join(werr, tmp.Chmod(0o644), tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir is best effort: some filesystems refuse to sync a directory.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
