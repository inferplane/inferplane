package main

import (
	"strings"
	"testing"
)

func classifierReloadConfig(dir, upstreamURL, revision string) string {
	base := reloadBaseConfig(dir, upstreamURL)
	block := `,"pii_classification":{"classifiers":[{"name":"laya-ko","type":"systemone","mode":"Shadow","revision":"` + revision +
		`","endpoint":"http://127.0.0.1:1","data_boundary":"internal"}]}}`
	return strings.TrimSuffix(strings.TrimSpace(base), "}") + block
}

func TestReloadRetainsOnlyTheCurrentClassifierRevision(t *testing.T) {
	up := newAnthropicUpstream(t)
	t.Setenv("E2E_UPSTREAM_KEY", e2eUpstreamKey)
	t.Setenv("E2E_ADMIN_TOKEN", e2eAdminToken)
	dir := t.TempDir()
	cfgPath := dir + "/config.json"
	rewriteConfig(t, cfgPath, classifierReloadConfig(dir, up.srv.URL, "r1"))
	g, err := newGateway(cfgPath)
	if err != nil {
		t.Fatalf("newGateway: %v", err)
	}
	if g.holder.Load().Classifiers() == nil {
		t.Fatal("boot did not build the classifier Set")
	}
	g.metrics.ObservePIIClassifier("laya-ko", "r1", "Shadow", "clear", 0.01, 1, 1, 1)
	rewriteConfig(t, cfgPath, classifierReloadConfig(dir, up.srv.URL, "r2"))
	if err := g.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	families, err := g.metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if !strings.HasPrefix(mf.GetName(), "inferplane_pii_classifier_decisions") {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "revision" && l.GetValue() == "r1" {
					t.Fatal("a replaced revision's series survived the reload")
				}
			}
		}
	}
}
