package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var bom = []byte("\xEF\xBB\xBF")

// decode parses data strictly: comment lines are blanked, unknown fields and
// anything after the object are errors.
func decode(data []byte, source string) (*Config, error) {
	text := stripComments(data)
	if t := bytes.TrimSpace(text); len(t) == 0 || t[0] != '{' {
		return nil, fmt.Errorf("%s: the config must be a JSON object", source)
	}

	dec := json.NewDecoder(bytes.NewReader(text))
	dec.DisallowUnknownFields()
	c := &Config{}
	if err := dec.Decode(c); err != nil {
		return nil, positioned(source, text, err)
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, fmt.Errorf("%s: unexpected data after the top-level object", source)
	}
	return c, nil
}

// stripComments empties the lines whose first non-blank characters are "//",
// keeping every newline so that error positions match the original.
func stripComments(data []byte) []byte {
	lines := bytes.Split(bytes.TrimPrefix(data, bom), []byte("\n"))
	for i, l := range lines {
		if bytes.HasPrefix(bytes.TrimSpace(l), []byte("//")) {
			lines[i] = nil
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

// positioned adds line:column to errors that carry a byte offset.
func positioned(source string, text []byte, err error) error {
	var offset int64
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		offset = syn.Offset
	case errors.As(err, &typ):
		offset = typ.Offset
	default:
		return fmt.Errorf("%s: %w", source, err)
	}
	// Offset counts the bytes read, including the offending one.
	end := min(max(int(offset)-1, 0), len(text))
	line := 1 + bytes.Count(text[:end], []byte("\n"))
	col := end - bytes.LastIndexByte(text[:end], '\n')
	return fmt.Errorf("%s:%d:%d: %w", source, line, col, err)
}
