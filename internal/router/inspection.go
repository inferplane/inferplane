package router

import (
	"context"
	"errors"

	"github.com/inferplane/inferplane/internal/sensitivity"
)

var errInspectionEvidence = errors.New("router: full inspection evidence missing")

func inspectForRoute(ctx context.Context, inspector sensitivity.Inspector, protocol string, raw []byte, mode sensitivity.InspectionMode) (sensitivity.Result, error) {
	if selective, ok := inspector.(sensitivity.SelectiveInspector); ok {
		result, err := selective.InspectWithMode(ctx, protocol, raw, mode)
		if err == nil && mode == sensitivity.DetectSensitiveData && !result.SensitiveDataInspected {
			return sensitivity.Result{}, errInspectionEvidence
		}
		return result, err
	}
	// The original interface promises full inspection. Old injected inspectors
	// do that work even for a structural request and cannot supply the new bit.
	result, err := inspector.Inspect(ctx, protocol, raw)
	if err == nil {
		result.SensitiveDataInspected = true
	}
	return result, err
}
