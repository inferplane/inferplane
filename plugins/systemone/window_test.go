package systemone

import (
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// fnvBoundary is a deterministic stand-in for the classifier's keyed hash.
func fnvBoundary(u string) bool {
	h := fnv.New32a()
	h.Write([]byte(u))
	return h.Sum32()%boundaryEvery == 0
}

func never(string) bool { return false }

func always(string) bool { return true }

func TestBuildWindowsPacksShortUnits(t *testing.T) {
	got := buildWindows([]string{"a", "", "bc"}, 10, 2, never)
	if !slices.Equal(got, []string{"a\n\nbc"}) {
		t.Fatalf("%q", got)
	}
}

func TestBuildWindowsFlushesWhenFull(t *testing.T) {
	got := buildWindows([]string{"aaaa", "bbbb"}, 6, 0, never)
	if !slices.Equal(got, []string{"aaaa", "bbbb"}) {
		t.Fatalf("%q", got)
	}
}

func TestBuildWindowsSplitsAnOversizedUnit(t *testing.T) {
	long := strings.Repeat("가", 25)
	got := buildWindows([]string{"short", long}, 10, 2, never)
	if got[0] != "short" {
		t.Fatalf("the short unit was not flushed before the split: %q", got)
	}
	if !slices.Equal(got[1:], splitRunes(long, 10, 2)) {
		t.Fatalf("split windows %q", got[1:])
	}
	for _, w := range got {
		if utf8.RuneCountInString(w) > 10 {
			t.Fatalf("window over size: %q", w)
		}
	}
}

func TestBuildWindowsEndsAWindowAtAContentBoundary(t *testing.T) {
	marker := func(u string) bool { return u == "M" }
	got := buildWindows([]string{"x", "M", "y"}, 100, 0, marker)
	if !slices.Equal(got, []string{"x\n\nM", "M\n\ny"}) {
		t.Fatalf("%q", got)
	}
}

// Security regression (2026-10-16 commit review): a filler unit that is a
// boundary must not separate two nearby parts.
func TestACraftedBoundaryCannotSeparateNearbyParts(t *testing.T) {
	filler := func(u string) bool { return strings.HasPrefix(u, "filler") }
	units := []string{"환자 서지수", "filler-1", "filler-2", "진단: 우울증"}
	for _, w := range buildWindows(units, 200, 150, filler) {
		if strings.Contains(w, "서지수") && strings.Contains(w, "우울증") {
			return
		}
	}
	t.Fatal("boundary fillers separated the name from the diagnosis")
}

// Any two short units within overlap runes of each other share a window,
// whatever the boundary function decides.
func TestBuildWindowsCoReadUnitsWithinTheOverlap(t *testing.T) {
	units := make([]string, 300)
	for i := range units {
		units[i] = fmt.Sprintf("<%d|%s>", i, strings.Repeat("내용", i%7))
	}
	const size, overlap = 200, 60
	for name, b := range map[string]func(string) bool{"fnv": fnvBoundary, "always": always, "never": never} {
		windows := buildWindows(units, size, overlap, b)
		for i := range units {
			span := utf8.RuneCountInString(units[i])
			for j := i + 1; j < len(units); j++ {
				span += 2 + utf8.RuneCountInString(units[j])
				if span > overlap {
					break
				}
				together := false
				for _, w := range windows {
					if strings.Contains(w, units[i]) && strings.Contains(w, units[j]) {
						together = true
						break
					}
				}
				if !together {
					t.Fatalf("%s: units %d and %d (span %d <= %d) never share a window", name, i, j, span, overlap)
				}
			}
		}
	}
}

// A conversation grows by appending turns, and a client may place later
// fields (tool schemas) after the messages. Content-defined boundaries keep
// the windows before and after a change identical, so their cache entries
// keep hitting; size-only packing does not.
func TestBuildWindowsStayStableAsAConversationGrows(t *testing.T) {
	units := make([]string, 300)
	for i := range units {
		units[i] = fmt.Sprintf("메시지 %d: 이 함수의 오류 처리를 검토해 주세요.", i)
	}
	grown := slices.Insert(slices.Clone(units), 3, strings.Repeat("중간에 끼어든 새 메시지 ", 4))
	keptAfterInsert := func(b func(string) bool) (int, int) {
		before := buildWindows(units, 200, 20, b)
		after := buildWindows(grown, 200, 20, b)
		kept := 0
		for _, w := range before {
			if slices.Contains(after, w) {
				kept++
			}
		}
		return kept, len(before)
	}
	before := buildWindows(units, 200, 20, fnvBoundary)
	appended := buildWindows(append(slices.Clone(units), "새 질문"), 200, 20, fnvBoundary)
	if !slices.Equal(appended[:len(before)-1], before[:len(before)-1]) {
		t.Fatal("appending a turn changed earlier windows")
	}
	kept, total := keptAfterInsert(fnvBoundary)
	if kept < total*9/10 {
		t.Fatalf("an early insertion kept only %d of %d windows", kept, total)
	}
	if sizeOnly, n := keptAfterInsert(never); sizeOnly*10 >= n*9 {
		t.Fatalf("size-only packing also kept %d of %d windows; the fixture does not exercise boundaries", sizeOnly, n)
	}
}

func TestBuildWindowsCoverEveryUnit(t *testing.T) {
	units := []string{"a", strings.Repeat("나", 33), "", "bc", strings.Repeat("다", 7)}
	joined := strings.Join(buildWindows(units, 10, 3, fnvBoundary), "|")
	for _, u := range units {
		for _, r := range u {
			if !strings.ContainsRune(joined, r) {
				t.Fatalf("rune %q of unit %q is in no window", r, u)
			}
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
