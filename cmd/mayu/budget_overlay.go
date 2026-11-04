package main

// restrictiveBudget composes independent admission caps for one window.
// Only explicit warn is soft; omitted on_exceeded has the governor's default
// blocking meaning. Soft meters cannot erase or narrow a hard cap.
// fromOverlay reports that the overlay's limit binds, so the denial it
// produces names the overlay as its source (ADR-057).
func restrictiveBudget(base int64, baseMode string, overlay int64, overlayMode string) (limit int64, mode string, fromOverlay bool) {
	if overlay == 0 {
		return base, baseMode, false
	}
	if base == 0 {
		return overlay, overlayMode, true
	}
	baseHard, overlayHard := baseMode != "warn", overlayMode != "warn"
	if baseHard != overlayHard {
		if overlayHard {
			return overlay, "block", true
		}
		return base, "block", false
	}
	mode = "warn"
	if baseHard {
		mode = "block"
	}
	if overlay < base {
		return overlay, mode, true
	}
	return base, mode, false
}
