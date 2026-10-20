//go:build liveclassifier

package systemone_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/sensitivity"
	_ "github.com/inferplane/inferplane/plugins/systemone"
)

const codingSystem = "You are a coding assistant working in a Go repository. Follow the repository conventions, run the tests before you report, and keep answers short."

// wrap places a row's text where coding traffic carries it: a tool result
// after a harmless first turn, behind a system prompt and two tool schemas.
func wrap(text string) []byte {
	body := map[string]any{
		"system": codingSystem,
		"tools": []map[string]any{
			{"name": "read_file", "description": "Read a file from the workspace.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
			{"name": "run", "description": "Run a shell command.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}}},
		},
		"messages": []map[string]any{
			{"role": "user", "content": "이 파일 내용을 요약해줘."},
			{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "t1", "name": "read_file", "input": map[string]any{"path": "notes.txt"}}}},
			{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": text}}},
		},
		"max_tokens": 256,
	}
	b, _ := json.Marshal(body)
	return b
}

//	Run: INFERPLANE_LIVE_SYSTEMONE_URL=http://127.0.0.1:18095 INFERPLANE_LIVE_ROWS=<rows_v3.jsonl> INFERPLANE_LIVE_EXCLUDE=<disputed_ids_v3.txt> \
//	  go test -tags liveclassifier ./plugins/systemone/ -run TestLiveWrappedBodies -v -timeout 3h
func TestLiveWrappedBodies(t *testing.T) {
	url, rows := os.Getenv("INFERPLANE_LIVE_SYSTEMONE_URL"), os.Getenv("INFERPLANE_LIVE_ROWS")
	if url == "" || rows == "" {
		t.Skip("INFERPLANE_LIVE_SYSTEMONE_URL and INFERPLANE_LIVE_ROWS not set")
	}
	exclude := map[string]bool{}
	if path := os.Getenv("INFERPLANE_LIVE_EXCLUDE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range strings.Fields(string(raw)) {
			exclude[id] = true
		}
	}
	c, err := classifier.New(classifier.Config{Name: "laya-ko", Type: "systemone", Endpoint: url, Threshold: 0.5,
		Options: map[string]json.RawMessage{"model": json.RawMessage(`"multilingual"`)}})
	if err != nil {
		t.Fatal(err)
	}
	set, err := classifier.NewSet([]classifier.Entry{{Name: "laya-ko", Mode: classifier.Enforce, Classifier: c, Global: true}}, 2*time.Minute, 4<<20, 100_000, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(rows)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var tp, fp, fn, tn, abstain int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r struct {
			ID    string `json:"id"`
			Text  string `json:"text"`
			Label int    `json:"label"`
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if exclude[r.ID] {
			continue
		}
		// The gateway reports every entry as an error for a body whose text
		// cannot be extracted (internal/router/classify.go); a row that embeds
		// duplicate-key JSON, such as a notebook, is such a body. Mirror that
		// and count it with the abstain/error rows.
		res := classifier.Failed(set.Entries)[0]
		if segments, err := sensitivity.Segments(context.Background(), "anthropic", wrap(r.Text)); err == nil {
			res = set.Run(context.Background(), set.Entries, classifier.Request{Team: "eval", Segments: segments})[0]
		}
		flagged := res.Outcome == classifier.OutcomeFlagged
		if res.Outcome == classifier.OutcomeAbstain || res.Outcome == classifier.OutcomeError {
			abstain++
		}
		switch {
		case flagged && r.Label == 1:
			tp++
		case flagged:
			fp++
		case r.Label == 1:
			fn++
		default:
			tn++
		}
	}
	f1 := 2 * float64(tp) / float64(2*tp+fp+fn)
	fpr := float64(fp) / float64(fp+tn)
	t.Logf("wrapped bodies: n=%d F1=%.3f FPR=%.3f abstain/error=%d (document-level reference, laya-ko-pii-v4s2: F1 0.962, FPR 0.048)", tp+fp+fn+tn, f1, fpr, abstain)
	if f1 < 0.90 || fpr > 0.08 {
		t.Fatalf("wrapped-body accuracy below the sanity floor: F1 %.3f FPR %.3f", f1, fpr)
	}
}
