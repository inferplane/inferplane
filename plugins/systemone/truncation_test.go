package systemone

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/classifier"
)

// wires are the two request shapes; opts selects one.
var wires = map[string]string{"batch": "", "single": `"batch":false`}

func withOpts(wire string, extra ...string) string {
	parts := slices.DeleteFunc(append([]string{wire}, extra...), func(s string) bool { return s == "" })
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// usageServer answers both wires. respond gives one window's probability and
// the raw usage JSON to return with it ("" leaves usage out).
func usageServer(t *testing.T, calls *atomic.Int64, respond func(state string) (float64, string)) *httptest.Server {
	t.Helper()
	result := func(state string) map[string]json.RawMessage {
		p, usage := respond(state)
		answers, _ := json.Marshal(map[string]any{"pii": map[string]any{"type": "noul", "noul": p}})
		out := map[string]json.RawMessage{"model": json.RawMessage(`"multilingual"`), "answers": answers}
		if usage != "" {
			out["usage"] = json.RawMessage(usage)
		}
		return out
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		var body struct {
			State  *string  `json:"state"`
			States []string `json:"states"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request body: %v", err)
		}
		switch r.URL.Path {
		case "/v1/systemone/batch":
			results := make([]map[string]json.RawMessage, len(body.States))
			for i, s := range body.States {
				results[i] = result(s)
			}
			json.NewEncoder(w).Encode(map[string]any{"results": results, "total_usage": map[string]int{"input_tokens": 1, "output_tokens": 0}})
		case "/v1/systemone":
			json.NewEncoder(w).Encode(result(*body.State))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fixed(p float64, usage string) func(string) (float64, string) {
	return func(string) (float64, string) { return p, usage }
}

const (
	wholeUsage = `{"input_tokens":120,"state_tokens":80,"state_tokens_dropped":0,"truncated":false,"truncated_questions":[]}`
	cutUsage   = `{"input_tokens":1024,"state_tokens":1359,"state_tokens_dropped":377,"truncated":true,"truncated_questions":["pii"]}`
)

func verdictsFor(t *testing.T, opts string, respond func(string) (float64, string), windows ...string) ([]classifier.Verdict, error) {
	t.Helper()
	out, err := newClassifier(t, usageServer(t, nil, respond).URL, opts, 0.5, "").Score(context.Background(), windows)
	return out.Verdicts, err
}

// Ruling 21: laya-serve cuts an over-budget state without an error and says so
// in usage. A window it did not read whole is never Clear.
func TestTruncatedWindowIsNeverClear(t *testing.T) {
	for wire, opt := range wires {
		for name, usage := range map[string]string{
			"truncated":               cutUsage,
			"state_tokens_dropped":    `{"truncated":false,"state_tokens_dropped":5}`,
			"truncated without count": `{"truncated":true}`,
		} {
			got, err := verdictsFor(t, withOpts(opt), fixed(0.01, usage), "a long window")
			if err != nil || !slices.Equal(got, []classifier.Verdict{classifier.Abstain}) {
				t.Errorf("%s, %s: %v %v, want Abstain", wire, name, got, err)
			}
		}
		got, err := verdictsFor(t, withOpts(opt), fixed(0.01, wholeUsage), "a short window")
		if err != nil || !slices.Equal(got, []classifier.Verdict{classifier.Clear}) {
			t.Errorf("%s, read whole: %v %v, want Clear", wire, got, err)
		}
	}
}

// The part that was read already contains personal data, so a flag stands.
func TestTruncatedWindowStillFlags(t *testing.T) {
	for wire, opt := range wires {
		got, err := verdictsFor(t, withOpts(opt), fixed(0.99, cutUsage), "a long window")
		if err != nil || !slices.Equal(got, []classifier.Verdict{classifier.Flagged}) {
			t.Errorf("%s: %v %v, want Flagged", wire, got, err)
		}
	}
}

// A band verdict on a cut window stays Abstain.
func TestTruncatedWindowInTheBandAbstains(t *testing.T) {
	got, err := verdictsFor(t, `{"abstain_band":{"low":0.1,"high":0.9}}`, fixed(0.5, cutUsage), "a long window")
	if err != nil || !slices.Equal(got, []classifier.Verdict{classifier.Abstain}) {
		t.Fatalf("%v %v", got, err)
	}
}

// truncation "reported" (the default): a result that does not report whether
// the window was cut is not used.
func TestUnreportedTruncationAbstainsByDefault(t *testing.T) {
	for wire, opt := range wires {
		for name, usage := range map[string]string{
			"no usage":           "",
			"null usage":         `null`,
			"tokens only":        `{"input_tokens":120,"output_tokens":0}`,
			"dropped only":       `{"state_tokens_dropped":0}`,
			"dropped, no flag":   `{"state_tokens_dropped":5}`,
			"null truncated":     `{"truncated":null,"state_tokens_dropped":0}`,
			"explicitly default": "",
		} {
			opts := withOpts(opt)
			if name == "explicitly default" {
				opts = withOpts(opt, `"truncation":"reported"`)
			}
			for _, p := range []float64{0.01, 0.99} {
				got, err := verdictsFor(t, opts, fixed(p, usage), "a window")
				if err != nil || !slices.Equal(got, []classifier.Verdict{classifier.Abstain}) {
					t.Errorf("%s, %s, p %v: %v %v, want Abstain", wire, name, p, got, err)
				}
			}
		}
	}
}

// truncation "rejected": the operator asserts the server refuses over-length
// input with an HTTP error, so a missing report is a normal verdict; a report
// of truncation is still honoured.
func TestRejectedTruncationJudgesUnreportedResults(t *testing.T) {
	for wire, opt := range wires {
		opts := withOpts(opt, `"truncation":"rejected"`)
		for p, want := range map[float64]classifier.Verdict{0.01: classifier.Clear, 0.99: classifier.Flagged} {
			got, err := verdictsFor(t, opts, fixed(p, ""), "a window")
			if err != nil || !slices.Equal(got, []classifier.Verdict{want}) {
				t.Errorf("%s, p %v: %v %v, want %v", wire, p, got, err, want)
			}
		}
		for _, usage := range []string{cutUsage, `{"state_tokens_dropped":3}`} {
			got, err := verdictsFor(t, opts, fixed(0.01, usage), "a window")
			if err != nil || !slices.Equal(got, []classifier.Verdict{classifier.Abstain}) {
				t.Errorf("%s, reported cut %s: %v %v, want Abstain", wire, usage, got, err)
			}
		}
		// strands-decider --strict-window answers an over-length state with 422.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(422) }))
		t.Cleanup(srv.Close)
		if _, err := newClassifier(t, srv.URL, opts, 0.5, "").Score(context.Background(), []string{"a window"}); !errors.Is(err, classifier.ErrStatus) {
			t.Errorf("%s: a refused window gave %v, want a status error", wire, err)
		}
	}
}

func TestMalformedUsageIsADecodeError(t *testing.T) {
	for wire, opt := range wires {
		for _, usage := range []string{`"truncated"`, `[]`, `1`, `{"truncated":"yes"}`, `{"truncated":1}`, `{"state_tokens_dropped":1.5}`, `{"state_tokens_dropped":"3"}`, `{"state_tokens_dropped":-1}`} {
			for _, mode := range []string{"", `"truncation":"rejected"`} {
				_, err := verdictsFor(t, withOpts(opt, mode), fixed(0.01, usage), "a window")
				if !errors.Is(err, classifier.ErrDecode) {
					t.Errorf("%s %s, usage %s: err = %v, want decode", wire, mode, usage, err)
				}
			}
		}
	}
}

func TestTruncationOption(t *testing.T) {
	for _, v := range []string{`"reported"`, `"rejected"`} {
		if _, err := New(classifier.Config{Name: "laya-ko", Type: "systemone", Endpoint: "http://127.0.0.1:1", Threshold: 0.5, Options: options(t, `{"truncation":`+v+`}`)}); err != nil {
			t.Errorf("truncation %s: %v", v, err)
		}
	}
	for _, v := range []string{`"guess"`, `"Reported"`, `""`, `null`, `true`, `1`} {
		_, err := New(classifier.Config{Name: "laya-ko", Type: "systemone", Endpoint: "http://127.0.0.1:1", Threshold: 0.5, Options: options(t, `{"truncation":`+v+`}`)})
		if err == nil || !strings.Contains(err.Error(), "option truncation") {
			t.Errorf("truncation %s: err = %v", v, err)
		}
	}
}

// The option changes answers, so it is part of every cache key.
func TestSettingsDigestCoversTruncation(t *testing.T) {
	base := classifier.Config{Name: "laya-ko", Type: "systemone", Endpoint: "http://127.0.0.1:1", Threshold: 0.5}
	reported, rejected := base, base
	reported.Options = options(t, `{"truncation":"reported"}`)
	rejected.Options = options(t, `{"truncation":"rejected"}`)
	if classifier.SettingsDigest(reported) == classifier.SettingsDigest(rejected) {
		t.Fatal("truncation modes share a settings digest")
	}
}

// Through Set.Run: a request whose only window was cut is never Clear, and its
// window is never cached, so every request reads it again.
func TestRunNeverClearsOrCachesATruncatedWindow(t *testing.T) {
	var calls atomic.Int64
	srv := usageServer(t, &calls, func(state string) (float64, string) {
		if strings.HasPrefix(state, "long") {
			return 0.0032, cutUsage
		}
		return 0.01, wholeUsage
	})
	set, err := classifier.NewSet([]classifier.Entry{{Name: "laya-ko", Mode: classifier.Enforce, Classifier: newClassifier(t, srv.URL, "", 0.5, ""), Global: true}}, 5*time.Second, 1<<20, 100, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	run := func(segment string) classifier.Result {
		return set.Run(context.Background(), set.Entries, classifier.Request{Team: "kr", Segments: []string{segment}})[0]
	}
	for i := 0; i < 2; i++ {
		if r := run("long padded state ... 010-2345-6789"); r.Outcome != classifier.OutcomeAbstain || r.CacheHits != 0 {
			t.Fatalf("run %d: %+v, want abstain with no cache hit", i, r)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("a cut window was cached: %d calls for two requests", calls.Load())
	}
	for i, want := range []int{0, 1} {
		if r := run("short clean state"); r.Outcome != classifier.OutcomeClear || r.CacheHits != want {
			t.Fatalf("control run %d: %+v", i, r)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("a whole clear window was not cached: %d calls", calls.Load())
	}
}
