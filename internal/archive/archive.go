// Package archive stores each day's edition once. The create-if-absent write
// is the idempotency lock that keeps a day's email from going out twice.
package archive

import (
	"context"
	"fmt"
	"time"
)

// Archive stores editions by date.
type Archive interface {
	// PutIfAbsent stores data as the edition for date (YYYY-MM-DD). It reports
	// created=false, err=nil when that date already exists: the caller must
	// not resend.
	PutIfAbsent(ctx context.Context, date string, data []byte) (created bool, err error)
}

type noop struct{}

// NewNoop returns an Archive that stores nothing and never locks.
func NewNoop() Archive { return noop{} }

func (noop) PutIfAbsent(ctx context.Context, date string, _ []byte) (bool, error) {
	if err := check(ctx, date); err != nil {
		return false, err
	}
	return true, nil
}

// check rejects a done context and anything but a real YYYY-MM-DD date, which
// also rules out path traversal.
func check(ctx context.Context, date string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if t, err := time.Parse(time.DateOnly, date); err != nil || t.Format(time.DateOnly) != date {
		return fmt.Errorf("archive: invalid date %q", date)
	}
	return nil
}
