package archive

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type fileArchive struct{ dir string }

// NewFile returns an Archive writing {dir}/{date}.epub. dir is created on
// first use.
func NewFile(dir string) Archive { return &fileArchive{dir: dir} }

// PutIfAbsent writes a temp file and hard-links it into place, which fails if
// the edition exists. A crash never leaves a partial edition behind.
func (a *fileArchive) PutIfAbsent(ctx context.Context, date string, data []byte) (bool, error) {
	if err := check(ctx, date); err != nil {
		return false, err
	}
	final := filepath.Join(a.dir, date+".epub")
	created, err := a.link(final, data)
	if err != nil {
		return false, fmt.Errorf("archive %s: %w", final, err)
	}
	return created, nil
}

func (a *fileArchive) link(final string, data []byte) (bool, error) {
	if err := os.MkdirAll(a.dir, 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(a.dir, ".edition-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())

	_, werr := tmp.Write(data)
	// Every step runs so the file always gets closed; any failure aborts.
	if err := errors.Join(werr, tmp.Chmod(0o644), tmp.Sync(), tmp.Close()); err != nil {
		return false, err
	}
	err = os.Link(tmp.Name(), final)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	return err == nil, err
}
