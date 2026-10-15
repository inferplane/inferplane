package controlplane

import (
	"bytes"
	"net/http"

	"sigs.k8s.io/yaml"
)

// mountExport registers GET /v1alpha1/config/export on the policy Server:
// it renders the currently-distributed policy set back to multi-document
// GovernancePolicy YAML — the exact format all three delivery channels
// (local file, control-plane push, Helm ConfigMap) already share, so server
// replication and org-to-org handover are "save this output, point the next
// inferplaned's --policies at it." Secret-free by construction: policies
// carry budget/rate/modelAccess rules and secret REFS only, never values.
//
// Its inverse is POST /v1alpha1/config/import (import.go): a replace-mode,
// dry-run-first restore of exactly this format into the policy store.
func (s *Server) mountExport(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1alpha1/config/export", authn(s.token, s.authOpts, s.handleConfigExport))
}

func (s *Server) handleConfigExport(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	wire := s.wire
	gen := s.generation
	s.mu.Unlock()

	var buf bytes.Buffer
	for i := range wire {
		raw, err := yaml.Marshal(&wire[i])
		if err != nil {
			http.Error(w, `{"error":"could not render policies"}`, http.StatusInternalServerError)
			return
		}
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(raw)
	}
	short := gen
	if len(short) > 12 {
		short = short[:12]
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="inferplane-policies-`+short+`.yaml"`)
	_, _ = w.Write(buf.Bytes())
}
