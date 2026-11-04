package clientctx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
)

func hdr(class, session string) http.Header {
	h := http.Header{}
	if class != "" {
		h.Set("x-claude-code-request-class", class)
	}
	if session != "" {
		h.Set("X-Claude-Code-Session-Id", session)
	}
	return h
}

func TestRequestClassAllowList(t *testing.T) {
	for class, want := range map[string]string{"main": "main", "auxiliary": "auxiliary", "side_reply": "side_reply", "AUXILIARY": "", "debug": "", "": ""} {
		ref := FromRequest(hdr(class, ""), "ik_1", DefaultMode())
		got := ""
		if ref != nil {
			got = ref.RequestClass
		}
		if got != want {
			t.Fatalf("class %q -> %q, want %q", class, got, want)
		}
	}
}

func TestSessionRefIsKeyScopedDigest(t *testing.T) {
	const id = "0b6f6a8e-2f7c-4c1e-9a77-raw-session"
	a := FromRequest(hdr("main", id), "ik_a", DefaultMode())
	b := FromRequest(hdr("main", id), "ik_b", DefaultMode())
	if a.SessionRef == b.SessionRef {
		t.Fatal("same session under two keys must yield two refs")
	}
	if !regexp.MustCompile(`^s1:[0-9a-f]{32}$`).MatchString(a.SessionRef) || strings.Contains(a.SessionRef, "raw-session") {
		t.Fatalf("bad ref %q", a.SessionRef)
	}
	if a.SessionRef != SessionRef("ik_a", id) {
		t.Fatal("FromRequest and SessionRef disagree")
	}
}

func TestInvalidSessionIDsAreDropped(t *testing.T) {
	for _, id := range []string{"has space", strings.Repeat("a", 129), "semi;colon", "ünïcode"} {
		if ref := FromRequest(hdr("", id), "ik", DefaultMode()); ref != nil {
			t.Fatalf("session %q accepted: %+v", id, ref)
		}
	}
}

func TestSessionRefOffKeepsClass(t *testing.T) {
	ref := FromRequest(hdr("subagent", "abc"), "ik", Mode{SessionRef: false, AutoMode: true})
	if ref == nil || ref.RequestClass != "subagent" || ref.SessionRef != "" {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestMiddlewareAttachesEvidenceAndMode(t *testing.T) {
	var gotClient *audit.ClientRef
	var gotWanted bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClient = audit.ClientFrom(r.Context())
		gotWanted = AutoModeWanted(r.Context())
		rec := audit.Record{Event: "request_completed", Outcome: &audit.OutcomeRef{Status: 200}}
		audit.Finish(r.Context(), &rec)
		if rec.Latency == nil {
			t.Error("Finish found no collector")
		}
	})
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header = hdr("auxiliary", "sess-1")
	req = req.WithContext(principal.With(req.Context(), keystore.Principal{KeyID: "ik_m"}))
	Middleware(DefaultMode(), next).ServeHTTP(httptest.NewRecorder(), req)
	if gotClient == nil || gotClient.RequestClass != "auxiliary" || gotClient.SessionRef != SessionRef("ik_m", "sess-1") || !gotWanted {
		t.Fatalf("client=%+v wanted=%v", gotClient, gotWanted)
	}
}

func TestAutoModeWantedFollowsMode(t *testing.T) {
	ctx := audit.WithEvidence(context.Background(), time.Now(), &audit.ClientRef{RequestClass: "main"})
	if !AutoModeWanted(WithMode(ctx, DefaultMode())) {
		t.Fatal("DefaultMode must enable auto mode evidence for a main-class request")
	}
	if AutoModeWanted(WithMode(ctx, Mode{SessionRef: true})) {
		t.Fatal("auto_mode off must disable evidence")
	}
	if AutoModeWanted(ctx) {
		t.Fatal("a context with no mode must not enable evidence")
	}
}
