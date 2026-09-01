package controlplane

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inferplane/inferplane/internal/policy"
)

func TestVersionBelow(t *testing.T) {
	cases := []struct {
		v, min string
		want   bool
	}{
		{"v0.2.0", "v0.3.0", true},
		{"0.3.0", "v0.3.0", false},
		{"v0.3.1", "v0.3.0", false},
		{"v0.3.0", "v0.3.0", false},
		{"v1.0.0", "v0.9.9", false},
		{"v0.3", "v0.3.0", false},       // missing part reads as 0
		{"v0.3", "v0.3.1", true},        // ditto, still ordered
		{"v0.3.0-rc1", "v0.3.0", false}, // pre-release suffix ignored (advisory channel)
		{"dev", "v0.3.0", true},         // unparseable = stale by design
		{"", "v0.3.0", true},            // old build reporting nothing
		{"abc1234", "v0.3.0", true},     // git hash
	}
	for _, c := range cases {
		if got := versionBelow(c.v, c.min); got != c.want {
			t.Errorf("versionBelow(%q, %q) = %v, want %v", c.v, c.min, got, c.want)
		}
	}
}

func TestSyncVersionVisibilityAndUpdateAdvice(t *testing.T) {
	s, ts := newTestServer(t, "")

	// No minimum configured: version is stored, no advice.
	resp := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp-old", APIVersions: policy.SupportedAPIVersions, Version: "v0.2.0"})
	if resp.UpdateAdvice != nil {
		t.Fatalf("no minimum configured, but advice sent: %+v", resp.UpdateAdvice)
	}

	s.SetUpdateAdvice("v0.3.0", "https://example.com/releases")

	// Below minimum → advice with min and URL.
	resp = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp-old", Version: "v0.2.0"})
	if resp.UpdateAdvice == nil || resp.UpdateAdvice.MinVersion != "v0.3.0" || resp.UpdateAdvice.URL != "https://example.com/releases" {
		t.Fatalf("stale plane got no/mangled advice: %+v", resp.UpdateAdvice)
	}
	// At and above minimum → no advice.
	for _, v := range []string{"v0.3.0", "v0.4.1"} {
		resp = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp-new", Version: v})
		if resp.UpdateAdvice != nil {
			t.Fatalf("version %s is not stale, but advice sent: %+v", v, resp.UpdateAdvice)
		}
	}
	// Unparseable ("dev") → advice: the minimum exists to smoke these out.
	resp = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp-dev", Version: "dev"})
	if resp.UpdateAdvice == nil {
		t.Fatal("dev build must be judged stale when a minimum is set")
	}

	// The dataplanes view exposes the per-plane version distribution.
	hresp, err := http.Get(ts.URL + "/v1alpha1/dataplanes")
	if err != nil {
		t.Fatal(err)
	}
	defer hresp.Body.Close()
	var view struct {
		Dataplanes map[string]struct {
			Version string `json:"version"`
		} `json:"dataplanes"`
	}
	if err := json.NewDecoder(hresp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Dataplanes["dp-old"].Version != "v0.2.0" || view.Dataplanes["dp-new"].Version != "v0.4.1" || view.Dataplanes["dp-dev"].Version != "dev" {
		t.Fatalf("version distribution wrong: %+v", view.Dataplanes)
	}
}
