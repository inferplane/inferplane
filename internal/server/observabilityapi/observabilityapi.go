// Package observabilityapi is the observability console API (sub-project B of
// the console work): GET /admin/observability reports governed OTLP export
// status, and the edit routes narrow export or replace console-owned rules,
// dry run first. Console.Routes is mounted by server.WithFullAdminRoutes, so
// every route, reads included, needs a full administrator.
//
// The bootstrap config bounds every edit. Runtime limits only narrow the
// declared exporter ceiling (otelexport.Limit); console rules are
// GovernancePolicy documents in a console-owned file that the local policy
// file channel loads, so the existing ceiling check applies and, with a
// control plane, the control plane stays the only policy source. No route
// names, changes or reveals an endpoint credential.
package observabilityapi

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

// Policy sources the status reports.
const (
	SourceFiles        = "files"
	SourceControlPlane = "control_plane"
	SourceNone         = "none"
	sourceConsole      = "console"
)

// legacyExporter is tracing.LegacyExporter, the legacy `otel` block's name.
const legacyExporter = "otel"

// PolicyStore is the part of *policy.Store the console uses. Reload is called
// only after the console wrote its own policy file.
type PolicyStore interface {
	Policies() []*policy.Policy
	CheckLocal(policies []*policy.Policy) error
	Reload() error
}

// LegacyExporter describes the legacy `otel` block for the status view.
type LegacyExporter struct {
	Endpoint    string  `json:"endpoint"`
	Protocol    string  `json:"protocol"`
	TLS         bool    `json:"tls"`
	SampleRatio float64 `json:"sample_ratio"`
}

// Options wires one console. A nil Set reports configured=false, an empty
// StateFile keeps limits read-only and an empty PolicyFile keeps rules
// read-only. Policies must be a nil interface — not a nil *policy.Store —
// when the gateway has no policy store.
type Options struct {
	Set          *otelexport.Set
	Policies     PolicyStore
	PolicySource string // SourceFiles | SourceControlPlane | SourceNone
	StateFile    string
	PolicyFile   string
	ContentRules bool
	Legacy       *LegacyExporter
	Emit         func(audit.Record)
	AuditReady   func() (bool, string) // nil = ready
}

// Console serves the observability console routes. Edits are serialized.
type Console struct {
	opts Options
	mu   sync.Mutex
}

// New builds a console over opts.
func New(opts Options) *Console { return &Console{opts: opts} }

// Routes maps each console route pattern to its handler, for
// server.WithFullAdminRoutes.
func (c *Console) Routes() map[string]http.Handler {
	return map[string]http.Handler{
		"GET /admin/observability":        http.HandlerFunc(c.status),
		"PUT /admin/observability/limits": http.HandlerFunc(c.putLimits),
	}
}

func (c *Console) source() string {
	if c.opts.PolicySource == "" {
		return SourceNone
	}
	return c.opts.PolicySource
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
