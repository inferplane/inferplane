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

// A reload whose classifier block fails validation keeps serving the current
// generation's Set (and its cache), never a half-built one or none.
func TestRejectedClassifierReloadKeepsTheCurrentSet(t *testing.T) {
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
	before := g.holder.Load().Classifiers()
	if before == nil {
		t.Fatal("boot did not build the classifier Set")
	}
	next := classifierReloadConfig(dir, up.srv.URL, "r2")
	if strings.Count(next, `"data_boundary":"internal"`) != 1 {
		t.Fatal("fixture: the classifier boundary is not unique in the config")
	}
	for name, tc := range map[string]struct{ with, want string }{
		"plugin option": {`"data_boundary":"internal","options":{"truncation":"guess"}`, "option truncation"},
		"empty teams":   {`"data_boundary":"internal","teams":[]`, "teams must list at least one team"},
		"external":      {`"data_boundary":"external"`, "external classifiers are not supported"},
	} {
		rewriteConfig(t, cfgPath, strings.Replace(next, `"data_boundary":"internal"`, tc.with, 1))
		if err := g.reload(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: reload err = %v, want %q", name, err, tc.want)
		}
		after := g.holder.Load().Classifiers()
		if after != before || len(after.Entries) != 1 || after.Entries[0].Revision != "r1" {
			t.Fatalf("%s: a rejected reload replaced the classifier Set", name)
		}
	}
}
