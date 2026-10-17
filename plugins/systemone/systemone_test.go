package systemone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/classifier"
)

const bearer = "bearer-value-123"

func options(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	if raw == "" {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func newClassifier(t *testing.T, endpoint, opts string, threshold float64, token string) classifier.Classifier {
	t.Helper()
	c, err := New(classifier.Config{Name: "laya-ko", Type: "systemone", Endpoint: endpoint, Threshold: threshold, BearerToken: token, Options: options(t, opts)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// batchServer answers /v1/systemone/batch with one probability per state.
func batchServer(t *testing.T, seen func(r *http.Request, body map[string]any), probs func(states []string) []float64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request body: %v", err)
		}
		if seen != nil {
			seen(r, body)
		}
		var states []string
		for _, s := range body["states"].([]any) {
			states = append(states, s.(string))
		}
		var results []map[string]any
		for _, p := range probs(states) {
			results = append(results, map[string]any{"model": "laya-rl-agent", "answers": map[string]any{"pii": map[string]any{"type": "noul", "noul": p}}, "usage": json.RawMessage(wholeUsage)})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results, "total_usage": map[string]int{"input_tokens": 1}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func constant(p float64) func([]string) []float64 {
	return func(states []string) []float64 {
		out := make([]float64, len(states))
		for i := range out {
			out[i] = p
		}
		return out
	}
}

func TestRegistersSystemoneType(t *testing.T) {
	if !slices.Contains(classifier.Types(), "systemone") {
		t.Fatal("systemone is not registered")
	}
}

func TestBatchRequestCarriesTheTrainedQuestion(t *testing.T) {
	srv := batchServer(t, func(r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/systemone/batch" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" {
			t.Errorf("path %s, content-type %q, authorization %q", r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization"))
		}
		q := body["questions"].(map[string]any)["pii"].(map[string]any)
		if q["type"] != "noul" || q["instructions"] != defaultInstructions || q["criteria"] != nil || body["model"] != nil {
			t.Errorf("question %v, model %v", q, body["model"])
		}
	}, constant(0.01))
	out, err := newClassifier(t, srv.URL, "", 0.5, "").Score(context.Background(), []string{"hello"})
	if err != nil || !slices.Equal(out.Verdicts, []classifier.Verdict{classifier.Clear}) || out.Sent != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestRequestCarriesConfiguredSettingsVerbatim(t *testing.T) {
	srv := batchServer(t, func(r *http.Request, body map[string]any) {
		q := body["questions"].(map[string]any)["pii"].(map[string]any)
		crit := q["criteria"].(map[string]any)
		if r.Header.Get("Authorization") != "Bearer "+bearer || body["model"] != "multilingual" || q["instructions"] != "Is there PII?" || crit["true"] != "yes" || crit["false"] != "no" {
			t.Errorf("body %v", body)
		}
	}, constant(0.01))
	c := newClassifier(t, srv.URL+"/", `{"model":"multilingual","instructions":"Is there PII?","criteria":{"true":"yes","false":"no"}}`, 0.5, bearer)
	if _, err := c.Score(context.Background(), []string{"hello"}); err != nil {
		t.Fatal(err)
	}
}

func TestVerdictBoundaries(t *testing.T) {
	for _, tc := range []struct {
		opts string
		p    float64
		want classifier.Verdict
	}{
		{"", 0.4999, classifier.Clear},
		{"", 0.5, classifier.Flagged},
		{`{"abstain_band":{"low":0.1,"high":0.9}}`, 0.1, classifier.Clear},
		{`{"abstain_band":{"low":0.1,"high":0.9}}`, 0.1000001, classifier.Abstain},
		{`{"abstain_band":{"low":0.1,"high":0.9}}`, 0.5, classifier.Abstain},
		{`{"abstain_band":{"low":0.1,"high":0.9}}`, 0.9, classifier.Flagged},
		{`{"abstain_band":{"low":0.1,"high":0.9}}`, 0.05, classifier.Clear},
	} {
		srv := batchServer(t, nil, constant(tc.p))
		out, err := newClassifier(t, srv.URL, tc.opts, 0.5, "").Score(context.Background(), []string{"x"})
		if err != nil || len(out.Verdicts) != 1 || out.Verdicts[0] != tc.want {
			t.Errorf("opts %s p %v: %+v %v", tc.opts, tc.p, out, err)
		}
	}
}

func TestScoreReturnsOneVerdictPerWindow(t *testing.T) {
	srv := batchServer(t, nil, func(states []string) []float64 { return []float64{0.01, 0.99} })
	out, err := newClassifier(t, srv.URL, "", 0.5, "").Score(context.Background(), []string{"clean window", "flagged window"})
	if err != nil || !slices.Equal(out.Verdicts, []classifier.Verdict{classifier.Clear, classifier.Flagged}) || out.Sent != 2 {
		t.Fatalf("%+v %v", out, err)
	}
}

// Review Focus 1: read a prefix of windows; the rest are Abstain and unsent.
func TestMaxWindowsReadsPrefixAndAbstains(t *testing.T) {
	var states atomic.Int64
	srv := batchServer(t, func(_ *http.Request, body map[string]any) { states.Store(int64(len(body["states"].([]any)))) }, constant(0.01))
	out, err := newClassifier(t, srv.URL, `{"max_windows":2}`, 0.5, "").Score(context.Background(), []string{"one", "two", "three"})
	want := []classifier.Verdict{classifier.Clear, classifier.Clear, classifier.Abstain}
	if err != nil || !slices.Equal(out.Verdicts, want) || states.Load() != 2 || out.Sent != 2 {
		t.Fatalf("%+v %v states=%d", out, err, states.Load())
	}
}

func TestWindowsAreDeterministicAndBounded(t *testing.T) {
	c := newClassifier(t, "http://127.0.0.1:1", `{"window_runes":200,"window_overlap_runes":20}`, 0.5, "")
	segments := []string{"short", strings.Repeat("가", 450), "", "tail"}
	first, second := c.Windows(segments), c.Windows(segments)
	if !slices.Equal(first, second) || len(first) < 3 || !strings.HasPrefix(first[0], "short") {
		t.Fatalf("windows %d %q", len(first), first[:1])
	}
	for _, w := range first {
		if n := []rune(w); len(n) > 200 || len(n) == 0 {
			t.Fatalf("window of %d runes", len(n))
		}
	}
}

// Review Focus 3: every failure is a bounded class with no request text or token.
func TestErrorsAreBoundedClasses(t *testing.T) {
	const pii = "김민수 010-2345-6789"
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("a redirect was followed") }))
	t.Cleanup(other.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	for name, tc := range map[string]struct {
		handler http.HandlerFunc
		url     string
		timeout time.Duration
		want    error
	}{
		"500":          {handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, pii+" "+bearer, 500) }, want: classifier.ErrStatus},
		"503":          {handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }, want: classifier.ErrStatus},
		"redirect":     {handler: func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }, want: classifier.ErrStatus},
		"malformed":    {handler: func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"results":[`) }, want: classifier.ErrDecode},
		"count":        {handler: func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"results":[]}`) }, want: classifier.ErrDecode},
		"not noul":     {handler: answerWith(`{"type":"choice","choice":"a"}`), want: classifier.ErrDecode},
		"out of range": {handler: answerWith(`{"type":"noul","noul":1.5}`), want: classifier.ErrDecode},
		"missing":      {handler: answerWith(`{"type":"noul"}`), want: classifier.ErrDecode},
		"oversized": {handler: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"results":[{"answers":{"pii":{"type":"noul","noul":0.1}}}],"pad":"`+strings.Repeat("x", maxResponseBytes)+`"}`)
		}, want: classifier.ErrDecode},
		"refused": {url: closed.URL, want: classifier.ErrTransport},
		"timeout": {handler: func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body) // the server notices a closed client only after the body is read
			<-r.Context().Done()
		}, timeout: 50 * time.Millisecond, want: classifier.ErrTimeout},
	} {
		t.Run(name, func(t *testing.T) {
			url := tc.url
			if tc.handler != nil {
				srv := httptest.NewServer(tc.handler)
				t.Cleanup(srv.Close)
				url = srv.URL
			}
			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			out, err := newClassifier(t, url, "", 0.5, bearer).Score(ctx, []string{pii})
			if !errors.Is(err, tc.want) || out.Verdicts != nil {
				t.Fatalf("got %+v %v, want %v", out, err, tc.want)
			}
			if strings.Contains(err.Error(), "김민수") || strings.Contains(err.Error(), bearer) || strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("error leaks request data: %v", err)
			}
		})
	}
}

func answerWith(item string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"results":[{"answers":{"pii":`+item+`}}]}`)
	}
}

func TestSingleModeBoundsConcurrency(t *testing.T) {
	var inFlight, peak, calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n := inFlight.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/systemone" || body["state"] == nil {
			t.Errorf("single request %s %v", r.URL.Path, body)
		}
		io.WriteString(w, `{"answers":{"pii":{"type":"noul","noul":0.01}},"usage":`+wholeUsage+`}`)
	}))
	t.Cleanup(srv.Close)
	units := make([]string, 6)
	for i := range units {
		units[i] = strings.Repeat(string(rune('가'+i)), 150)
	}
	out, err := newClassifier(t, srv.URL, `{"batch":false,"concurrency":2}`, 0.5, "").Score(context.Background(), units)
	if err != nil || len(out.Verdicts) != 6 || slices.Contains(out.Verdicts, classifier.Flagged) || calls.Load() != 6 || peak.Load() > 2 {
		t.Fatalf("%+v %v calls=%d peak=%d", out, err, calls.Load(), peak.Load())
	}
}

func TestSingleModeFailsOnTheFirstError(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, `{"answers":{"pii":{"type":"noul","noul":0.01}},"usage":`+wholeUsage+`}`)
	}))
	t.Cleanup(srv.Close)
	_, err := newClassifier(t, srv.URL, `{"batch":false,"concurrency":1}`, 0.5, "").Score(context.Background(), []string{"first window", "second window"})
	if !errors.Is(err, classifier.ErrStatus) {
		t.Fatalf("err = %v", err)
	}
}

func TestNoWindowsMeansNoCall(t *testing.T) {
	c := newClassifier(t, "http://127.0.0.1:1", "", 0.5, "")
	if w := c.Windows([]string{"", ""}); len(w) != 0 {
		t.Fatalf("empty segments produced windows %q", w)
	}
	out, err := c.Score(context.Background(), nil)
	if err != nil || len(out.Verdicts) != 0 || out.Sent != 0 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestNewRejectsAThresholdOutsideTheOpenUnitInterval(t *testing.T) {
	for _, threshold := range []float64{0, 1, 1.5, -0.1, math.NaN()} {
		if _, err := New(classifier.Config{Name: "laya-ko", Type: "systemone", Endpoint: "http://127.0.0.1:1", Threshold: threshold}); err == nil || !strings.Contains(err.Error(), "threshold") {
			t.Errorf("threshold %v: err = %v", threshold, err)
		}
	}
}

func TestClientIgnoresProxiesAndRedirects(t *testing.T) {
	c := newClassifier(t, "http://127.0.0.1:1", "", 0.5, "").(*systemone)
	tr, ok := c.client.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil || c.client.CheckRedirect == nil {
		t.Fatal("classifier text could leave through a proxy or a redirect")
	}
	// Ruling 22: concurrent requests to one sidecar reuse connections instead
	// of churning through the default two idle connections per host.
	if tr.MaxIdleConnsPerHost != 16 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 16", tr.MaxIdleConnsPerHost)
	}
}

func TestOptionValidation(t *testing.T) {
	for opts, wantField := range map[string]string{
		`{"unknown":1}`: "unknown",
		`{"abstain_band":{"low":0.6,"high":0.9}}`:         "abstain_band",
		`{"abstain_band":{"low":0.1}}`:                    "abstain_band",
		`{"window_runes":200,"window_overlap_runes":101}`: "window_overlap_runes",
		`{"max_windows":65}`:                              "max_windows",
		`{"concurrency":0}`:                               "concurrency",
		`{"model":"Multi lingual"}`:                       "model",
		`{"instructions":""}`:                             "instructions",
		`{"criteria":{"true":"yes"}}`:                     "criteria",
		`{"batch":null}`:                                  "batch",
		`{"window_runes":4.5}`:                            "window_runes",
	} {
		_, err := New(classifier.Config{Name: "laya-ko", Type: "systemone", Endpoint: "http://127.0.0.1:1", Threshold: 0.5, Options: options(t, opts)})
		if err == nil || !strings.Contains(err.Error(), wantField) {
			t.Errorf("options %s: err = %v, want it to name %s", opts, err, wantField)
		}
	}
}

// The 2026-10-16 review cases: a name and a diagnosis, adjacent or with a
// boundary filler between them, must be judged together through the real
// windows and the cache.
func TestRunJudgesNearbyPartsTogetherThroughRealWindows(t *testing.T) {
	const name, diagnosis = "환자 서지수", "진단: 우울증"
	srv := batchServer(t, nil, func(states []string) []float64 {
		out := make([]float64, len(states))
		for i, s := range states {
			if strings.Contains(s, name) && strings.Contains(s, diagnosis) {
				out[i] = 0.99
			}
		}
		return out
	})
	c := newClassifier(t, srv.URL, `{"window_runes":200}`, 0.5, "")
	var filler string
	for i := 0; filler == ""; i++ {
		if f := fmt.Sprintf("참고 %d", i); c.(*systemone).boundary(f) {
			filler = f
		}
	}
	set, err := classifier.NewSet([]classifier.Entry{{Name: "laya-ko", Mode: classifier.Enforce, Classifier: c, Global: true}}, 5*time.Second, 1<<20, 100, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	run := func(segments ...string) classifier.Outcome {
		return set.Run(context.Background(), set.Entries, classifier.Request{Team: "kr", Segments: segments})[0].Outcome
	}
	for _, part := range []string{name, diagnosis, filler} {
		if got := run(part); got != classifier.OutcomeClear {
			t.Fatalf("part %q alone: %s", part, got)
		}
	}
	for _, segments := range [][]string{{name, diagnosis}, {name, filler, diagnosis}} {
		if got := run(segments...); got != classifier.OutcomeFlagged {
			t.Fatalf("%q were not judged together: %s", segments, got)
		}
	}
}

func TestBoundaryHashIsKeyedPerInstance(t *testing.T) {
	a := newClassifier(t, "http://127.0.0.1:1", "", 0.5, "").(*systemone)
	b := newClassifier(t, "http://127.0.0.1:1", "", 0.5, "").(*systemone)
	differ := false
	for i := 0; i < 2000; i++ {
		u := fmt.Sprintf("unit %d", i)
		if a.boundary(u) != a.boundary(u) {
			t.Fatal("one instance gave two answers for the same unit")
		}
		differ = differ || a.boundary(u) != b.boundary(u)
	}
	if !differ {
		t.Fatal("two instances share boundary decisions; the hash is not keyed")
	}
}
