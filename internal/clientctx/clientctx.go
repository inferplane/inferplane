// Package clientctx turns bounded client request headers into audit evidence
// (ADR-057): an allow-listed Claude Code request class and a key-scoped,
// domain-separated session digest. The raw session ID never leaves this
// package. It also detects Claude Code auto mode classifier verdicts
// (automode.go).
package clientctx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/principal"
)

const (
	headerSession   = "X-Claude-Code-Session-Id"
	headerClass     = "X-Claude-Code-Request-Class"
	maxSessionBytes = 128

	// ClassAuxiliary is the class Claude Code uses for auto mode classifier calls.
	ClassAuxiliary = "auxiliary"
)

// requestClasses is the closed set Claude Code 2.1.312 sends.
var requestClasses = map[string]bool{
	"main": true, "subagent": true, "workflow": true,
	"compaction": true, ClassAuxiliary: true, "side_reply": true,
}

// Mode selects which client evidence the gateway records.
type Mode struct {
	SessionRef bool
	AutoMode   bool
}

// DefaultMode records both, as ADR-057 decides.
func DefaultMode() Mode { return Mode{SessionRef: true, AutoMode: true} }

// FromRequest returns the bounded client evidence for one request, or nil.
func FromRequest(h http.Header, keyID string, m Mode) *audit.ClientRef {
	ref := audit.ClientRef{}
	if c := h.Get(headerClass); requestClasses[c] {
		ref.RequestClass = c
	}
	if m.SessionRef {
		if id := h.Get(headerSession); validSessionID(id) {
			ref.SessionRef = SessionRef(keyID, id)
		}
	}
	if ref == (audit.ClientRef{}) {
		return nil
	}
	return &ref
}

// SessionRef is the key-scoped, domain-separated session digest.
func SessionRef(keyID, sessionID string) string {
	sum := sha256.Sum256([]byte("inferplane-session-v1\x00" + keyID + "\x00" + sessionID))
	return "s1:" + hex.EncodeToString(sum[:16])
}

func validSessionID(id string) bool {
	if len(id) == 0 || len(id) > maxSessionBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}

type modeKey struct{}

// WithMode records the configured evidence mode on the request context.
func WithMode(ctx context.Context, m Mode) context.Context {
	return context.WithValue(ctx, modeKey{}, m)
}

// ModeFrom returns the configured mode; absent means everything off.
func ModeFrom(ctx context.Context) Mode {
	m, _ := ctx.Value(modeKey{}).(Mode)
	return m
}

// Middleware attaches request evidence. It runs inside KeyAuth so the key ID
// is known; the start time is taken before any handler work.
func Middleware(m Mode, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		p, _ := principal.From(r.Context())
		ctx := audit.WithEvidence(r.Context(), start, FromRequest(r.Header, p.KeyID, m))
		next.ServeHTTP(w, r.WithContext(WithMode(ctx, m)))
	})
}

// AutoModeWanted reports whether auto mode evidence collection is enabled
// for this request. Which responses qualify is decided by the auto mode
// tap (block calls, server safeguards, auxiliary requests).
func AutoModeWanted(ctx context.Context) bool { return ModeFrom(ctx).AutoMode }
