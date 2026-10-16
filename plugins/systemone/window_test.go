package systemone

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildWindowsPacksShortUnits(t *testing.T) {
	got := buildWindows([]string{"a", "", "bc"}, 10, 2)
	if !slices.Equal(got, []string{"a\n\nbc"}) {
		t.Fatalf("%q", got)
	}
}

func TestBuildWindowsFlushesWhenFull(t *testing.T) {
	got := buildWindows([]string{"aaaa", "bbbb"}, 6, 0)
	if !slices.Equal(got, []string{"aaaa", "bbbb"}) {
		t.Fatalf("%q", got)
	}
}

func TestBuildWindowsSplitsAnOversizedUnit(t *testing.T) {
	long := strings.Repeat("가", 25)
	got := buildWindows([]string{"short", long}, 10, 2)
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
	var marker string
	for i := 0; marker == ""; i++ {
		if u := fmt.Sprintf("u%d", i); boundary(u) {
			marker = u
		}
	}
	if boundary("x") || boundary("y") {
		t.Fatal("fixture units must not be boundaries")
	}
	got := buildWindows([]string{"x", marker, "y"}, 100, 0)
	if !slices.Equal(got, []string{"x\n\n" + marker, marker + "\n\ny"}) {
		t.Fatalf("%q", got)
	}
}

// A conversation grows by appending turns, and a client may place later
// fields (tool schemas) after the messages. Content-defined boundaries keep
// the windows before and after a change identical, so their cache entries
// keep hitting.
func TestBuildWindowsStayStableAsAConversationGrows(t *testing.T) {
	units := make([]string, 300)
	for i := range units {
		units[i] = fmt.Sprintf("메시지 %d: 이 함수의 오류 처리를 검토해 주세요.", i)
	}
	before := buildWindows(units, 200, 20)
	appended := buildWindows(append(slices.Clone(units), "새 질문"), 200, 20)
	if !slices.Equal(appended[:len(before)-1], before[:len(before)-1]) {
		t.Fatal("appending a turn changed earlier windows")
	}
	inserted := buildWindows(slices.Insert(slices.Clone(units), 3, strings.Repeat("중간에 끼어든 새 메시지 ", 4)), 200, 20)
	kept := 0
	for _, w := range before {
		if slices.Contains(inserted, w) {
			kept++
		}
	}
	if kept < len(before)*9/10 {
		t.Fatalf("an early insertion kept only %d of %d windows", kept, len(before))
	}
}

func TestBuildWindowsCoverEveryUnit(t *testing.T) {
	units := []string{"a", strings.Repeat("나", 33), "", "bc", strings.Repeat("다", 7)}
	joined := strings.Join(buildWindows(units, 10, 3), "|")
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

func TestBuildWindowsAlwaysPairAdjacentUnits(t *testing.T) {
	units := make([]string, 400)
	for i := range units {
		units[i] = fmt.Sprintf("턴 %d 내용", i)
	}
	windows := buildWindows(units, 200, 20)
	for i := 1; i < len(units); i++ {
		paired := false
		for _, w := range windows {
			if strings.Contains(w, units[i-1]+"\n\n"+units[i]) {
				paired = true
				break
			}
		}
		if !paired {
			t.Fatalf("units %d and %d never share a window", i-1, i)
		}
	}
}
