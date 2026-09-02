package bedrock

// Strip-table CI guard (strategy Phase 0a, the `mayu pricing check` mold):
// the two model→param strip tables silently change sampling semantics, and
// they used to live only in code, sourced from a one-off manual probe with
// no stored artifact. This test pins both tables to
// testdata/strip_tables.json — editing a table without updating the
// recorded artifact (and its probe date) fails CI, so every drift is an
// explicit, reviewable diff naming which models lose which params.

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestStripTablesMatchRecordedArtifact(t *testing.T) {
	raw, err := os.ReadFile("testdata/strip_tables.json")
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		Probed   string `json:"probed"`
		Converse []struct {
			Match  string   `json:"match"`
			Params []string `json:"params"`
		} `json:"converse_unsupported_inference"`
		Mantle []struct {
			Match  string   `json:"match"`
			Params []string `json:"params"`
		} `json:"mantle_chat_strip_params"`
	}
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.Probed == "" {
		t.Fatal("artifact must record the probe date its entries were verified on")
	}

	if len(artifact.Converse) != len(converseUnsupportedInference) {
		t.Fatalf("converse strip table has %d entries, artifact records %d — update testdata/strip_tables.json in the same commit as the table (and re-probe)", len(converseUnsupportedInference), len(artifact.Converse))
	}
	for i, want := range artifact.Converse {
		got := converseUnsupportedInference[i]
		if got.match != want.Match || !reflect.DeepEqual(got.params, want.Params) {
			t.Fatalf("converse strip table entry %d = {%q %v}, artifact records {%q %v} — update testdata/strip_tables.json in the same commit", i, got.match, got.params, want.Match, want.Params)
		}
	}

	if len(artifact.Mantle) != len(mantleChatStripParams) {
		t.Fatalf("mantle strip table has %d entries, artifact records %d — update testdata/strip_tables.json in the same commit (and re-probe)", len(mantleChatStripParams), len(artifact.Mantle))
	}
	for i, want := range artifact.Mantle {
		got := mantleChatStripParams[i]
		if got.match != want.Match || !reflect.DeepEqual(got.params, want.Params) {
			t.Fatalf("mantle strip table entry %d = {%q %v}, artifact records {%q %v} — update testdata/strip_tables.json in the same commit", i, got.match, got.params, want.Match, want.Params)
		}
	}
}
