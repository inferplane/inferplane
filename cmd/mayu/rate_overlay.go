package main

// restrictiveRate composes independently configured upper bounds. Zero means
// this layer adds no rate limit; it never removes a nonzero limit underneath.
func restrictiveRate(base, overlay int64) int64 {
	if base == 0 || (overlay > 0 && overlay < base) {
		return overlay
	}
	return base
}
