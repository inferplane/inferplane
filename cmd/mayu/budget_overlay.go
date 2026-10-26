package main

// restrictiveBudget composes independent admission caps for one window.
// Only explicit warn is soft; omitted on_exceeded has the governor's default
// blocking meaning. Soft meters cannot erase or narrow a hard cap.
func restrictiveBudget(base int64, baseMode string, overlay int64, overlayMode string) (int64, string) {
	if overlay == 0 {
		return base, baseMode
	}
	if base == 0 {
		return overlay, overlayMode
	}
	baseHard, overlayHard := baseMode != "warn", overlayMode != "warn"
	if baseHard != overlayHard {
		if overlayHard {
			return overlay, "block"
		}
		return base, "block"
	}
	mode := "warn"
	if baseHard {
		mode = "block"
	}
	if overlay < base {
		return overlay, mode
	}
	return base, mode
}
