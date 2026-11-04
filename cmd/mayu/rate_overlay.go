package main

// restrictiveRate composes independently configured upper bounds. Zero means
// this layer adds no rate limit; it never removes a nonzero limit underneath.
// fromOverlay reports that the overlay's value binds, so the denial it
// produces names the overlay's rule (ADR-057).
func restrictiveRate(base, overlay int64) (limit int64, fromOverlay bool) {
	if base == 0 || (overlay > 0 && overlay < base) {
		return overlay, overlay > 0
	}
	return base, false
}
