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

// joined is the text buildWindows cuts: non-empty units joined by a blank line.
func joined(units []string) string {
	var parts []string
	for _, u := range units {
		if u != "" {
			parts = append(parts, u)
		}
	}
	return strings.Join(parts, "\n\n")
}

// offsets returns each window's start rune offset in the joined text.
func offsets(windows []string, overlap int) []int {
	out := make([]int, len(windows))
	for i := 1; i < len(windows); i++ {
		out[i] = out[i-1] + utf8.RuneCountInString(windows[i-1]) - overlap
	}
	return out
}

// coRead reports whether some window wholly contains runes [a, b) of the text.
func coRead(windows []string, overlap, a, b int) bool {
	for i, s := range offsets(windows, overlap) {
		if s <= a && b <= s+utf8.RuneCountInString(windows[i]) {
			return true
		}
	}
	return false
}

func fixtures() map[string][]string {
	long := strings.Repeat("가나다라마바사\n", 60)
	return map[string][]string{
		"short":   {"a", "", "bc"},
		"mixed":   {"role", "user", "", long, "환자 서지수", strings.Repeat("😀a", 300), "진단: 우울증", "끝"},
		"tiny":    strings.Split(strings.Repeat("x,", 500), ","),
		"oneLong": {long + long},
	}
}

func TestBuildWindowsFitShortTextInOneWindow(t *testing.T) {
	if got := buildWindows([]string{"a", "", "bc"}, 10, 2, never); !slices.Equal(got, []string{"a\n\nbc"}) {
		t.Fatalf("%q", got)
	}
}

func TestBuildWindowsPreferUnitEnds(t *testing.T) {
	if got := buildWindows([]string{"aaaa", "bbbb"}, 6, 0, never); !slices.Equal(got, []string{"aaaa", "\n\nbbbb"}) {
		t.Fatalf("%q", got)
	}
}

func TestBuildWindowsEndAtABoundaryUnit(t *testing.T) {
	marker := func(u string) bool { return u == "M" }
	if got := buildWindows([]string{"x", "M", "y"}, 6, 0, marker); !slices.Equal(got, []string{"x\n\nM", "\n\ny"}) {
		t.Fatalf("%q", got)
	}
}

func TestBuildWindowsPreferALateNewline(t *testing.T) {
	got := buildWindows([]string{strings.Repeat("a", 8) + "\n" + strings.Repeat("b", 20)}, 10, 2, never)
	if got[0] != strings.Repeat("a", 8)+"\n" {
		t.Fatalf("first window %q", got[0])
	}
}

// Every window is valid, within size and non-empty; consecutive windows share
// exactly overlap runes; and the windows rebuild the joined text exactly.
func TestBuildWindowsOverlapAndCoverTheText(t *testing.T) {
	for name, units := range fixtures() {
		for _, size := range []int{5, 13, 200, 1000} {
			for _, overlap := range []int{0, 1, size / 4, size / 2} {
				for bname, b := range map[string]func(string) bool{"never": never, "always": always, "fnv": fnvBoundary} {
					windows := buildWindows(units, size, overlap, b)
					rebuilt := ""
					for i, w := range windows {
						r := []rune(w)
						if len(r) == 0 || len(r) > size || !utf8.ValidString(w) {
							t.Fatalf("%s size %d overlap %d %s: bad window %d %q", name, size, overlap, bname, i, w)
						}
						if i == 0 {
							rebuilt = w
							continue
						}
						prev := []rune(windows[i-1])
						if string(prev[len(prev)-overlap:]) != string(r[:overlap]) {
							t.Fatalf("%s size %d overlap %d %s: windows %d and %d do not share the overlap", name, size, overlap, bname, i-1, i)
						}
						rebuilt += string(r[overlap:])
					}
					if rebuilt != joined(units) {
						t.Fatalf("%s size %d overlap %d %s: windows do not rebuild the text", name, size, overlap, bname)
					}
				}
			}
		}
	}
}

// Review cases (2026-10-16): crafted boundaries and long units must not
// separate a name and a diagnosis that are within the overlap of each other.
func TestNothingSeparatesNearbyParts(t *testing.T) {
	const name, diagnosis = "환자 서지수", "진단: 우울증"
	pad := func(n int) string { return strings.Repeat("앞선 대화 내용입니다. ", n) }
	filler := func(u string) bool { return strings.HasPrefix(u, "filler") }
	cases := map[string][]string{
		"crafted fillers":            {pad(40), name, "filler-1", "filler-2", diagnosis, pad(40)},
		"long unit after the name":   {"role", "user", "content", name, "role", "user", "content", diagnosis + strings.Repeat("가", 1000)},
		"long unit before the name":  {strings.Repeat("나", 1200) + name, diagnosis},
		"near-size diagnosis":        {pad(30), name, diagnosis + strings.Repeat("다", 990)},
		"name at a window edge":      {pad(45), name, diagnosis, pad(45)},
		"many boundary units around": append(append(strings.Split(strings.Repeat("filler,", 300), ","), name, diagnosis), strings.Split(strings.Repeat("filler,", 300), ",")...),
	}
	for cname, units := range cases {
		for bname, b := range map[string]func(string) bool{"never": never, "always": always, "fnv": fnvBoundary, "filler": filler} {
			text := []rune(joined(units))
			a := strings.Index(string(text), name)
			a = utf8.RuneCountInString(string(text)[:a])
			bEnd := strings.Index(string(text), diagnosis)
			bEnd = utf8.RuneCountInString(string(text)[:bEnd]) + utf8.RuneCountInString(diagnosis)
			if bEnd-a > 150 {
				t.Fatalf("%s: fixture span %d exceeds the overlap", cname, bEnd-a)
			}
			if !coRead(buildWindows(units, 1000, 150, b), 150, a, bEnd) {
				t.Fatalf("%s (%s boundaries): the name and the diagnosis never share a window", cname, bname)
			}
		}
	}
}

// Security regression (third commit security review, 2026-10-16): many tiny
// units that are all boundaries must not multiply the text the classifier
// reads or the gateway hashes.
func TestBuildWindowsBoundTheirTotalSize(t *testing.T) {
	units := make([]string, 20000)
	for i := range units {
		units[i] = fmt.Sprint(i % 10)
	}
	units = append(units, strings.Repeat("긴 붙여넣기 ", 4000))
	input := utf8.RuneCountInString(joined(units))
	const size = 1000
	for bname, b := range map[string]func(string) bool{"always": always, "fnv": fnvBoundary, "never": never} {
		for _, overlap := range []int{150, size / 2} {
			total := 0
			for _, w := range buildWindows(units, size, overlap, b) {
				total += utf8.RuneCountInString(w)
			}
			if total > 3*input+size {
				t.Fatalf("%s, overlap %d: %d runes of windows for %d runes of input", bname, overlap, total, input)
			}
		}
	}
}

// A conversation grows by appending turns, and a client may place later
// fields (tool schemas) after the messages. Content-defined boundaries keep
// the windows before and after a change identical, so their cache entries
// keep hitting; size-only cutting does not.
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
		t.Fatalf("size-only cutting also kept %d of %d windows; the fixture does not exercise boundaries", sizeOnly, n)
	}
}

func TestBuildWindowsNeverBreakARune(t *testing.T) {
	s := strings.Repeat("가a😀\n김", 50)
	for size := 5; size <= 13; size++ {
		for overlap := 0; overlap <= size/2; overlap++ {
			for _, w := range buildWindows([]string{s, "끝"}, size, overlap, fnvBoundary) {
				if !utf8.ValidString(w) || utf8.RuneCountInString(w) > size {
					t.Fatalf("size %d overlap %d: bad window %q", size, overlap, w)
				}
			}
		}
	}
}
