package state

import (
	"context"
	"errors"
)

// Store persists the state between runs.
type Store interface {
	Load(ctx context.Context) (*State, error)
	Save(ctx context.Context, s *State) error
}

// ErrConflict means the stored state changed since it was loaded.
var ErrConflict = errors.New("state: modified concurrently")
