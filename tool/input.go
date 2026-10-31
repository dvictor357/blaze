package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// decodeInput strictly decodes a tool's JSON input into v: unknown fields,
// wrong types and trailing data are errors. Blaze does not validate inputs
// against tool schemas, so every built-in tool validates through this and its
// own domain checks.
func decodeInput(input json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("invalid input: unexpected data after JSON object")
	}
	return nil
}
