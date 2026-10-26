package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestE2ESoftThresholdDoesNotPrematurelyBlockHardBudget(t *testing.T) {
	for _, period := range []string{"CalendarMonth", "CalendarDay"} {
		t.Run(period, func(t *testing.T) {
			up := newAnthropicUpstream(t)
			dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
				teamsAPIConfig(up.srv.URL)(cfg, dir)
				cfg["models"].(map[string]any)["claude-test"].(map[string]any)["context_window"] = 100
				dimension := "usd_per_month"
				if period == "CalendarDay" {
					dimension = "usd_per_day"
				}
				cfg["teams"] = map[string]any{"threshold-team": map[string]any{
					"allowed_models": []any{"*"},
					"budget":         map[string]any{dimension: 1000, "on_exceeded": "block"},
				}}
				document := fmt.Sprintf(`apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: {name: switching-meter}
spec:
  subject: {team: threshold-team}
  rules:
  - name: threshold
    failurePolicy: FailOpen
    budget: {period: %s, limitMilliUSD: 1}
`, period)
				path := filepath.Join(dir, "threshold.yaml")
				if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
					t.Fatal(err)
				}
				cfg["policies"] = []any{path}
			})
			_, key := createKey(t, adminURL, "threshold-team", []string{"*"})
			for i := range 2 {
				resp := postMessages(t, dataURL, key, "claude-test")
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("request %d after crossing soft threshold: status=%d body=%s", i+1, resp.StatusCode, body)
				}
			}
		})
	}
}
