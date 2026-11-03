package main

// ADR-057 end to end: a governance denial's audit record names the limit that
// refused it, for each layer that can set a team or key rate limit.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/controlplane"
)

// createKeyJSON issues a key from a raw /admin/keys body and returns its
// plaintext.
func createKeyJSON(t *testing.T, adminURL, body string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, adminURL+"/admin/keys", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e2eAdminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !strings.HasPrefix(out.Plaintext, "ik_") {
		t.Fatalf("create key: status %d err %v", resp.StatusCode, err)
	}
	return out.Plaintext
}

// writeRatePolicy writes a GovernancePolicy whose rule "rpm" limits gov-team.
func writeRatePolicy(t *testing.T, dir string, rpm int) string {
	t.Helper()
	doc := fmt.Sprintf(`apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: {name: team-rate, generation: 2}
spec:
  subject: {team: gov-team}
  rules:
  - name: rpm
    failurePolicy: FailOpen
    rate: {rpm: %d}
`, rpm)
	path := filepath.Join(dir, "rate.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// denialRecordIn returns the first gateway denial with the given status in an
// audit file, after checking the file's chain.
func denialRecordIn(t *testing.T, auditPath string, status int) audit.Record {
	t.Helper()
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := audit.Verify(bytes.NewReader(raw)); err != nil || !res.OK {
		t.Fatalf("audit chain broken: %+v %v", res, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var r audit.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Event == "request_started" && r.Outcome != nil && r.Outcome.Status == status {
			return r
		}
	}
	t.Fatalf("no %d denial record", status)
	return audit.Record{}
}

// rateDenialRecord boots a gateway, lets setup add limits and issue a key,
// sends two Messages requests and returns the 429 denial record.
func rateDenialRecord(t *testing.T, mutate func(cfg map[string]any, dir string), setup func(adminURL string) string) audit.Record {
	t.Helper()
	up := newAnthropicUpstream(t)
	var auditPath string
	dataURL, adminURL, shutdown := bootGateway(t, func(cfg map[string]any, dir string) {
		withAnthropicProvider(up.srv.URL)(cfg, dir)
		auditPath = filepath.Join(dir, "audit.jsonl")
		if mutate != nil {
			mutate(cfg, dir)
		}
	})
	key := setup(adminURL)
	for i, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		resp := postMessages(t, dataURL, key, "claude-test")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("request %d status = %d, want %d", i+1, resp.StatusCode, want)
		}
	}
	shutdown()
	return denialRecordIn(t, auditPath, http.StatusTooManyRequests)
}

func TestE2EGovernanceDenialNamesItsLimit(t *testing.T) {
	teamKey := func(adminURL string) string {
		_, key := createKey(t, adminURL, "gov-team", []string{"*"})
		return key
	}
	configTeam := func(rpm int) map[string]any {
		return map[string]any{"gov-team": map[string]any{"allowed_models": []any{"*"}, "rate_limit": map[string]any{"requests_per_minute": rpm}}}
	}
	for _, tc := range []struct {
		name   string
		mutate func(cfg map[string]any, dir string)
		setup  func(adminURL string) string
		want   audit.GovernanceRef
		rule   *audit.PolicyRuleRef
	}{
		{
			name: "policy rule narrower than config",
			mutate: func(cfg map[string]any, dir string) {
				cfg["teams"] = configTeam(100)
				cfg["policies"] = []any{writeRatePolicy(t, dir, 1)}
			},
			setup: teamKey,
			want:  audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitRPM, Source: audit.GovSourcePolicy},
			rule:  &audit.PolicyRuleRef{Name: "team-rate", Generation: 2, Rule: "rpm"},
		},
		{
			name: "config narrower than policy",
			mutate: func(cfg map[string]any, dir string) {
				cfg["teams"] = configTeam(1)
				cfg["policies"] = []any{writeRatePolicy(t, dir, 100)}
			},
			setup: teamKey,
			want:  audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitRPM, Source: audit.GovSourceConfig},
		},
		{
			name: "team record",
			setup: func(adminURL string) string {
				resp := putTeam(t, adminURL, "gov-team", `{"rpm":1}`)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("PUT team: %d", resp.StatusCode)
				}
				return teamKey(adminURL)
			},
			want: audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitRPM, Source: audit.GovSourceTeamRecord},
		},
		{
			name: "key option",
			setup: func(adminURL string) string {
				return createKeyJSON(t, adminURL, `{"team":"gov-team","allowed_models":["*"],"rpm":1}`)
			},
			want: audit.GovernanceRef{Scope: audit.GovScopeKey, Limit: audit.GovLimitRPM, Source: audit.GovSourceKey},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := rateDenialRecord(t, tc.mutate, tc.setup)
			g := r.Governance
			if g == nil || g.Scope != tc.want.Scope || g.Limit != tc.want.Limit || g.Source != tc.want.Source {
				t.Fatalf("governance = %+v, want %+v", g, tc.want)
			}
			if (g.Policy == nil) != (tc.rule == nil) || (g.Policy != nil && *g.Policy != *tc.rule) {
				t.Fatalf("policy = %+v, want %+v", g.Policy, tc.rule)
			}
		})
	}
}

// A hard cap distributed by the control plane binds through its lease
// (ADR-034): once local spend passes the granted allowance, the 402 names the
// lease, not the policy rule the lease was granted for.
func TestE2ELeaseBoundDenialNamesTheLease(t *testing.T) {
	t.Setenv("GD_CP_TOKEN", "gd-tok")
	polDir := t.TempDir()
	doc := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: lease-cap }
spec:
  subject: { team: gov-team }
  rules:
  - name: monthly
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1000000, hardCap: true, lease: { renewInterval: "1s" } }
`
	if err := os.WriteFile(filepath.Join(polDir, "p.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer("gd-tok", polDir)
	if err != nil {
		t.Fatal(err)
	}
	cpMux := http.NewServeMux()
	cp.Mount(cpMux)
	cpSrv := httptest.NewServer(cpMux)
	defer cpSrv.Close()

	up := newAnthropicUpstream(t)
	var auditPath string
	dataURL, adminURL, shutdown := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		auditPath = filepath.Join(dir, "audit.jsonl")
		cfg["control_plane"] = map[string]any{
			"url":       cpSrv.URL,
			"token_ref": map[string]any{"env": "GD_CP_TOKEN"},
			"dataplane": "gd-dp",
		}
	})
	_, key := createKey(t, adminURL, "gov-team", []string{"*"})
	// Wait for the first heartbeat to install the policy and its lease.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if limit, ok := budgetLimitMicros(getUsage(t, dataURL, key), "team_budget"); ok && limit < 1_000_000_000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease never clamped the team budget")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// teamsAPIConfig prices a token at $1, so the first request spends far
	// past the 0.1% allowance and the second is refused.
	for i, want := range []int{http.StatusOK, http.StatusPaymentRequired} {
		resp := postMessages(t, dataURL, key, "claude-test")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("request %d status = %d, want %d", i+1, resp.StatusCode, want)
		}
	}
	shutdown()
	r := denialRecordIn(t, auditPath, http.StatusPaymentRequired)
	g := r.Governance
	if g == nil || g.Scope != audit.GovScopeTeam || g.Source != audit.GovSourceLease || g.Policy != nil ||
		(g.Limit != audit.GovLimitBudgetMonth && g.Limit != audit.GovLimitBudget) {
		t.Fatalf("governance = %+v", g)
	}
}
