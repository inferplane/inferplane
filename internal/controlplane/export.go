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
// The import endpoint is deliberately reserved (spec D5): until a
// UI-editable policy store exists, import == placing these files in
// --policies.
func (s *Server) mountExport(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1alpha1/config/export", authn(s.token, s.authOpts, s.handleConfigExport))
}

func (s *Server) handleConfigExport(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	wire := s.wire
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
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}
