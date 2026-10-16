package systemone

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildWindowsPacksShortUnits(t *testing.T) {
	got := buildWindows([]string{"a", "", "bc"}, 10, 2)
	if len(got) != 1 || got[0].text != "a\n\nbc" || !slices.Equal(got[0].parts, []int{0, 2}) {
		t.Fatalf("%+v", got)
	}
}

func TestBuildWindowsFlushesWhenFull(t *testing.T) {
	got := buildWindows([]string{"aaaa", "bbbb"}, 6, 0)
	if len(got) != 2 || got[0].text != "aaaa" || got[1].text != "bbbb" || got[1].parts[0] != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestBuildWindowsSplitsAnOversizedUnit(t *testing.T) {
	got := buildWindows([]string{"short", strings.Repeat("가", 25)}, 10, 2)
	for i, w := range got[1:] {
		if !slices.Equal(w.parts, []int{1}) || utf8.RuneCountInString(w.text) > 10 {
			t.Fatalf("window %d %+v", i+1, w)
		}
	}
}

func TestSplitRunesPrefersALateNewline(t *testing.T) {
	s := strings.Repeat("a", 8) + "\n" + strings.Repeat("b", 20)
	got := splitRunes(s, 10, 2)
	if got[0] != strings.Repeat("a", 8)+"\n" {
		t.Fatalf("first window %q", got[0])
	}
}

func TestSplitRunesOverlapsAndCoversEverything(t *testing.T) {
	s := strings.Repeat("가나다라마바사\n", 40) + "끝"
	for _, size := range []int{5, 7, 12, 40} {
		for overlap := 0; overlap <= size/2; overlap++ {
			got := splitRunes(s, size, overlap)
			rebuilt := got[0]
			for _, w := range got[1:] {
				prev := []rune(rebuilt)
				if !strings.HasPrefix(w, string(prev[len(prev)-overlap:])) {
					t.Fatalf("size %d overlap %d: window does not repeat the overlap", size, overlap)
				}
				rebuilt += string([]rune(w)[overlap:])
			}
			if rebuilt != s {
				t.Fatalf("size %d overlap %d: windows do not cover the text", size, overlap)
			}
		}
	}
}

func TestSplitRunesNeverBreaksARune(t *testing.T) {
	s := strings.Repeat("가a😀\n김", 50)
	for size := 5; size <= 13; size++ {
		for overlap := 0; overlap <= size/2; overlap++ {
			for _, w := range splitRunes(s, size, overlap) {
				if !utf8.ValidString(w) || utf8.RuneCountInString(w) > size {
					t.Fatalf("size %d overlap %d: bad window %q", size, overlap, w)
				}
			}
		}
	}
}
