package config

import (
	"encoding/json"
	"log/slog"
)

// Secret is a string that never prints itself: fmt, slog and JSON all show
// "[redacted]". Convert with string(s) to use the value.
type Secret string

func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

func (s Secret) GoString() string { return `config.Secret("` + s.String() + `")` }

func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }
