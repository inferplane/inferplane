package maskingapi

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/filter"
	"github.com/inferplane/inferplane/internal/principal"
)

func TestMaskingRequiresAdminAndFreshRevision(t *testing.T) {
	c, err := filter.OpenControl(filepath.Join(t.TempDir(), "masking.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	h := New(c, nil, nil)
	call := func(admin bool, body string) int {
		r := httptest.NewRequest("PUT", "/admin/masking", strings.NewReader(body))
		if admin {
			r = r.WithContext(principal.WithAdmin(r.Context(), principal.AdminIdentity{IsAdmin: true}))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	body := `{"enabled":true,"expected_revision":"` + c.Snapshot().Revision + `"}`
	if got := call(false, body); got != 403 || c.Snapshot().Enabled {
		t.Fatalf("anonymous edit: %d", got)
	}
	if got := call(true, body); got != 200 || !c.Snapshot().Enabled {
		t.Fatalf("admin edit: %d", got)
	}
	if got := call(true, body); got != 409 {
		t.Fatalf("stale edit: %d", got)
	}
	if got := call(true, `{"expected_revision":"`+c.Snapshot().Revision+`"}`); got != 400 || !c.Snapshot().Enabled {
		t.Fatalf("missing enabled defaulted to disable: %d", got)
	}
}
