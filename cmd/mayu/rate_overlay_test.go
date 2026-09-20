package main

import "testing"

func TestRestrictiveRate(t *testing.T) {
	for _, tc := range []struct{ base, overlay, want int64 }{
		{0, 0, 0}, {10, 0, 10}, {0, 10, 10}, {10, 1000, 10}, {1000, 10, 10}, {10, 10, 10},
	} {
		if got := restrictiveRate(tc.base, tc.overlay); got != tc.want {
			t.Fatalf("base=%d overlay=%d got=%d want=%d", tc.base, tc.overlay, got, tc.want)
		}
	}
}
