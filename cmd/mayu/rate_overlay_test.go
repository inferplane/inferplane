package main

import "testing"

func TestRestrictiveRate(t *testing.T) {
	for _, tc := range []struct {
		base, overlay, want int64
		fromOverlay         bool
	}{
		{0, 0, 0, false}, {10, 0, 10, false}, {0, 10, 10, true}, {10, 1000, 10, false}, {1000, 10, 10, true}, {10, 10, 10, false},
	} {
		got, fromOverlay := restrictiveRate(tc.base, tc.overlay)
		if got != tc.want || fromOverlay != tc.fromOverlay {
			t.Fatalf("base=%d overlay=%d got=%d,%v want=%d,%v", tc.base, tc.overlay, got, fromOverlay, tc.want, tc.fromOverlay)
		}
	}
}
