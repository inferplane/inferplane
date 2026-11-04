package configapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCapabilitiesCarryObservabilityExport(t *testing.T) {
	h := CapabilitiesHandler(func() Capabilities { return Capabilities{ObservabilityExport: true} })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/capabilities", nil))
	if !strings.Contains(rec.Body.String(), `"observability_export":true`) {
		t.Fatalf("capabilities = %s", rec.Body)
	}
}
