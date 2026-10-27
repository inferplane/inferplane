package sensitivity

import (
	"context"
	"errors"
)

// InspectionMode controls finite sensitive-data detection, never JSON traversal
// or structural validation. Its zero value preserves full inspection.
type InspectionMode uint8

const (
	DetectSensitiveData InspectionMode = iota
	StructureOnly
)

// SelectiveInspector is optional: existing Inspector implementations keep the
// full inspection contract. Callers requiring privacy must check full evidence.
type SelectiveInspector interface {
	Inspector
	InspectWithMode(context.Context, string, []byte, InspectionMode) (Result, error)
}

var errInspectionMode = errors.New("sensitivity: invalid inspection mode")
