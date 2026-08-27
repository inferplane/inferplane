package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"

	"github.com/inferplane/inferplane/internal/config"
)

// fakePricingAPI serves canned PriceList pages, so the resolve/unresolved
// partition is testable with no AWS credentials and no network.
type fakePricingAPI struct {
	pages      map[string][][]string // regionCode -> pages of PriceList documents
	err        error
	calls      int
	gotFilters []string // "<field>=<value>" for every filter of every call
}

func (f *fakePricingAPI) GetProducts(ctx context.Context, in *awspricing.GetProductsInput, optFns ...func(*awspricing.Options)) (*awspricing.GetProductsOutput, error) {
	f.calls++
	region := ""
	for _, flt := range in.Filters {
		f.gotFilters = append(f.gotFilters, aws.ToString(flt.Field)+"="+aws.ToString(flt.Value))
		if aws.ToString(flt.Field) == "regionCode" {
			region = aws.ToString(flt.Value)
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	page := 0
	if in.NextToken != nil && *in.NextToken != "" {
		n, err := strconv.Atoi(*in.NextToken)
		if err != nil {
			return nil, fmt.Errorf("fake: bad NextToken %q", *in.NextToken)
		}
		page = n
	}
	// An unknown region returns an empty PriceList (not an error) — that
	// models a region with no Bedrock catalogue.
	pages := f.pages[region]
	out := &awspricing.GetProductsOutput{}
	if page < len(pages) {
		out.PriceList = pages[page]
	}
	if page+1 < len(pages) {
		out.NextToken = aws.String(strconv.Itoa(page + 1))
	}
	return out, nil
}

// VERIFIED: fetched from the AWS Price List Query API on 2026-08-26 with
// ServiceCode=AmazonBedrock, and trimmed to the fields priceCandidateFrom
// reads. The prices are AWS's real published figures, not invented ones.
const (
	// zai.glm-5, ap-northeast-1: the plain on-demand pair.
	docGLMInput  = `{"product":{"sku":"XWC25SAQMK2UTMY9","attributes":{"regionCode":"ap-northeast-1","inferenceType":"Input tokens","usagetype":"APN1-zai.glm5-input-tokens","model":"GLM 5","feature":"On-demand Inference"}},"terms":{"OnDemand":{"XWC25SAQMK2UTMY9.JRTCKXETXF":{"priceDimensions":{"XWC25SAQMK2UTMY9.JRTCKXETXF.6YS6EN2CT7":{"unit":"1K tokens","pricePerUnit":{"USD":"0.0012000000"}}}}}}}`
	docGLMOutput = `{"product":{"sku":"25DCN3EFASRTQK5W","attributes":{"regionCode":"ap-northeast-1","inferenceType":"Output tokens","usagetype":"APN1-zai.glm5-output-tokens","model":"GLM 5","feature":"On-demand Inference"}},"terms":{"OnDemand":{"25DCN3EFASRTQK5W.JRTCKXETXF":{"priceDimensions":{"25DCN3EFASRTQK5W.JRTCKXETXF.6YS6EN2CT7":{"unit":"1K tokens","pricePerUnit":{"USD":"0.0038400000"}}}}}}}`
	// The service-tier twins: same prices, different SKUs. Must NOT be a conflict.
	docGLMInputStandard  = `{"product":{"sku":"C5W95R8WUA6B2U55","attributes":{"regionCode":"ap-northeast-1","inferenceType":"Input tokens","usagetype":"APN1-zai.glm5-mantle-input-tokens-standard","model":"GLM 5","service_tier":"standard"}},"terms":{"OnDemand":{"C5W95R8WUA6B2U55.JRTCKXETXF":{"priceDimensions":{"C5W95R8WUA6B2U55.JRTCKXETXF.6YS6EN2CT7":{"unit":"1K tokens","pricePerUnit":{"USD":"0.0012000000"}}}}}}}`
	docGLMOutputStandard = `{"product":{"sku":"GH9C9DEN4PJQGGSM","attributes":{"regionCode":"ap-northeast-1","inferenceType":"Output tokens","usagetype":"APN1-zai.glm5-mantle-output-tokens-standard","model":"GLM 5","service_tier":"standard"}},"terms":{"OnDemand":{"GH9C9DEN4PJQGGSM.JRTCKXETXF":{"priceDimensions":{"GH9C9DEN4PJQGGSM.JRTCKXETXF.6YS6EN2CT7":{"unit":"1K tokens","pricePerUnit":{"USD":"0.0038400000"}}}}}}}`
	// flex: rejected by the inferenceType gate AND the tail whitelist.
	docGLMInputFlex = `{"product":{"sku":"CNU44JCVYWVHJMM5","attributes":{"regionCode":"ap-northeast-1","inferenceType":"Input tokens flex","usagetype":"APN1-zai.glm5-mantle-input-tokens-flex","model":"GLM 5","service_tier":"flex"}},"terms":{"OnDemand":{"CNU44JCVYWVHJMM5.JRTCKXETXF":{"priceDimensions":{"CNU44JCVYWVHJMM5.JRTCKXETXF.6YS6EN2CT7":{"unit":"1K tokens","pricePerUnit":{"USD":"0.0006000000"}}}}}}}`
	// Nova Pro, ap-northeast-1: the vendor prefix is DROPPED in usagetype.
	docNovaInput  = `{"product":{"sku":"MW5E2RRQFRGR96C3","attributes":{"regionCode":"ap-northeast-1","inferenceType":"Input tokens","usagetype":"APN1-NovaPro-input-tokens","model":"Nova Pro","feature":"On-demand Inference"}},"terms":{"OnDemand":{"MW5E2RRQFRGR96C3.JRTCKXETXF":{"priceDimensions":{"MW5E2RRQFRGR96C3.JRTCKXETXF.6YS6EN2CT7":{"unit":"1K tokens","pricePerUnit":{"USD":"0.0009600000"}}}}}}}`
	docNovaOutput = `{"product":{"sku":"NGEWXEWTZRVVAR4Q","attributes":{"regionCode":"ap-northeast-1","inferenceType":"Output tokens","usagetype":"APN1-NovaPro-output-tokens","model":"Nova Pro","feature":"On-demand Inference"}},"terms":{"OnDemand":{"NGEWXEWTZRVVAR4Q.JRTCKXETXF":{"priceDimensions":{"NGEWXEWTZRVVAR4Q.JRTCKXETXF.6YS6EN2CT7":{"unit":"1K tokens","pricePerUnit":{"USD":"0.0038400000"}}}}}}}`
	// The trap: inferenceType is a clean "Input tokens" but this is BATCH
	// inference at half the on-demand rate. Only the tail whitelist rejects it.
	docNovaInputBatch = `{"product":{"sku":"T4E5FXWGMWR4XVED","attributes":{"regionCode":"ap-northeast-1","inferenceType":"Input tokens","usagetype":"APN1-NovaPro-input-tokens-batch","model":"Nova Pro","feature":"Batch Inference"}},"terms":{"OnDemand":{"T4E5FXWGMWR4XVED.JRTCKXETXF":{"priceDimensions":{"T4E5FXWGMWR4XVED.JRTCKXETXF.6YS6EN2CT7":{"unit":"1K tokens","pricePerUnit":{"USD":"0.0004800000"}}}}}}}`
	// Prompt-cache row: rejected by the inferenceType gate.
	docNovaCacheRead = `{"product":{"sku":"C32RSZ93XHBRBRY7","attributes":{"regionCode":"ap-northeast-1","inferenceType":"Prompt cache read input tokens","usagetype":"APN1-NovaPro-cache-read-input-token-count","model":"Nova Pro","feature":"On-demand Inference"}},"terms":{"OnDemand":{"C32RSZ93XHBRBRY7.JRTCKXETXF":{"priceDimensions":{"C32RSZ93XHBRBRY7.JRTCKXETXF.6YS6EN2CT7":{"unit":"1K tokens","pricePerUnit":{"USD":"0.0002400000"}}}}}}}`
	// Claude 3 Haiku, us-east-1: an INPUT row with no output row anywhere.
	docClaudeHaikuInput = `{"product":{"sku":"KMZFVEBPVFAP5FUF","attributes":{"regionCode":"us-east-1","inferenceType":"Input tokens","usagetype":"USE1-Claude3Haiku-input-tokens","model":"Claude 3 Haiku","feature":"On-demand Inference"}},"terms":{"OnDemand":{"KMZFVEBPVFAP5FUF.JRTCKXETXF":{"priceDimensions":{"KMZFVEBPVFAP5FUF.JRTCKXETXF.6YS6EN2CT7":{"unit":"1K tokens","pricePerUnit":{"USD":"0.0002500000"}}}}}}}`
)

// allApne1Docs is every ap-northeast-1 fixture document above, nine in total —
// the resolvable rows plus the flex/batch/cache-read traps the filter must drop.
func allApne1Docs() []string {
	return []string{
		docGLMInput, docGLMOutput, docGLMInputStandard, docGLMOutputStandard,
		docGLMInputFlex, docNovaInput, docNovaOutput, docNovaInputBatch,
		docNovaCacheRead,
	}
}

// syncTestFragment mirrors the emitted fragment with json.RawMessage rates, so
// assertions read the EXACT bytes out of the raw JSON. Unmarshalling into
// float64 would round-trip through a float and hide exactly the rendering
// artifact ratToConfigNumber (§C6) exists to prevent.
type syncTestFragment struct {
	Pricing struct {
		Overrides map[string]map[string]struct {
			InputPerMTok  json.RawMessage `json:"input_per_mtok"`
			OutputPerMTok json.RawMessage `json:"output_per_mtok"`
		} `json:"overrides"`
	} `json:"pricing"`
}

func parseSyncFragment(t *testing.T, raw []byte) syncTestFragment {
	t.Helper()
	var frag syncTestFragment
	if err := json.Unmarshal(raw, &frag); err != nil {
		t.Fatalf("stdout is not the JSON fragment: %v\n%s", err, raw)
	}
	return frag
}

func TestRunPricingSync_resolvesAndPartitions(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"bedrock-apne1": {Type: "bedrock", Region: "ap-northeast-1"},
			"bedrock-use1":  {Type: "bedrock", Region: "us-east-1"},
			"selfhosted":    {Type: "openai_compatible", BaseURL: "http://localhost:8000/v1"},
		},
		Models: map[string]config.ModelConfig{
			"glm":    {Targets: []config.Target{{Provider: "bedrock-apne1", Model: "zai.glm-5"}}},
			"nova":   {Targets: []config.Target{{Provider: "bedrock-apne1", Model: "amazon.nova-pro-v1:0"}}},
			"claude": {Targets: []config.Target{{Provider: "bedrock-use1", Model: "anthropic.claude-sonnet-4-6"}}},
			"local":  {Targets: []config.Target{{Provider: "selfhosted", Model: "llama3.3"}}},
		},
	}
	api := &fakePricingAPI{pages: map[string][][]string{
		"ap-northeast-1": {allApne1Docs()},
		"us-east-1":      {{docClaudeHaikuInput}},
	}}
	var stdout, stderr bytes.Buffer
	if got := runPricingSync(&stdout, &stderr, cfg, api, ""); got != 1 {
		t.Fatalf("exit = %d, want 1 (one route unresolved)\nstderr: %s", got, stderr.String())
	}
	frag := parseSyncFragment(t, stdout.Bytes())
	apne1 := frag.Pricing.Overrides["bedrock-apne1"]
	if apne1 == nil {
		t.Fatalf("no bedrock-apne1 overrides in fragment:\n%s", stdout.String())
	}
	// Exact strings, not floats: the rendered number must be the clean decimal.
	if got := string(apne1["zai.glm-5"].InputPerMTok); got != "1.2" {
		t.Errorf("zai.glm-5 input_per_mtok = %q, want \"1.2\"", got)
	}
	if got := string(apne1["zai.glm-5"].OutputPerMTok); got != "3.84" {
		t.Errorf("zai.glm-5 output_per_mtok = %q, want \"3.84\"", got)
	}
	if got := string(apne1["amazon.nova-pro-v1:0"].InputPerMTok); got != "0.96" {
		t.Errorf("amazon.nova-pro-v1:0 input_per_mtok = %q, want \"0.96\"", got)
	}
	if got := string(apne1["amazon.nova-pro-v1:0"].OutputPerMTok); got != "3.84" {
		t.Errorf("amazon.nova-pro-v1:0 output_per_mtok = %q, want \"3.84\"", got)
	}
	// Belt and braces on the raw bytes too, so a RawMessage decoding quirk
	// cannot mask a float artifact in the rendered document.
	if !strings.Contains(stdout.String(), `"input_per_mtok": 1.2`) {
		t.Errorf("stdout missing the exact rendering %q:\n%s", `"input_per_mtok": 1.2`, stdout.String())
	}
	// The openai_compatible route is excluded by design (ADR-030 §5) and must
	// appear nowhere — not in the fragment and not in the unresolved report.
	combined := stdout.String() + stderr.String()
	for _, banned := range []string{"selfhosted", "llama3.3"} {
		if strings.Contains(combined, banned) {
			t.Errorf("openai_compatible route leaked into output: found %q", banned)
		}
	}
	if !strings.Contains(stderr.String(), "UNRESOLVED bedrock-use1/anthropic.claude-sonnet-4-6") {
		t.Errorf("stderr does not name the unresolved route:\n%s", stderr.String())
	}
}

func TestRunPricingSync_allResolvedExitsZero(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"bedrock-apne1": {Type: "bedrock", Region: "ap-northeast-1"},
		},
		Models: map[string]config.ModelConfig{
			"glm": {Targets: []config.Target{{Provider: "bedrock-apne1", Model: "zai.glm-5"}}},
		},
	}
	api := &fakePricingAPI{pages: map[string][][]string{"ap-northeast-1": {allApne1Docs()}}}
	var stdout, stderr bytes.Buffer
	if got := runPricingSync(&stdout, &stderr, cfg, api, ""); got != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", got, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr not empty: %s", stderr.String())
	}
}

// Two distinct SKUs at one identical price (the on-demand row and its
// `standard` service-tier twin) must resolve: ambiguity is only a failure when
// the prices DISAGREE.
func TestRunPricingSync_serviceTierTwinsAreNotAConflict(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"bedrock-apne1": {Type: "bedrock", Region: "ap-northeast-1"},
		},
		Models: map[string]config.ModelConfig{
			"glm": {Targets: []config.Target{{Provider: "bedrock-apne1", Model: "zai.glm-5"}}},
		},
	}
	api := &fakePricingAPI{pages: map[string][][]string{
		"ap-northeast-1": {{docGLMInput, docGLMOutput, docGLMInputStandard, docGLMOutputStandard}},
	}}
	var stdout, stderr bytes.Buffer
	if got := runPricingSync(&stdout, &stderr, cfg, api, ""); got != 0 {
		t.Fatalf("exit = %d, want 0 — two SKUs at one price must resolve\nstderr: %s", got, stderr.String())
	}
	rate := parseSyncFragment(t, stdout.Bytes()).Pricing.Overrides["bedrock-apne1"]["zai.glm-5"]
	if got := string(rate.InputPerMTok); got != "1.2" {
		t.Errorf("input_per_mtok = %q, want \"1.2\"", got)
	}
	if got := string(rate.OutputPerMTok); got != "3.84" {
		t.Errorf("output_per_mtok = %q, want \"3.84\"", got)
	}
}

// The tail-whitelist regression test: APN1-NovaPro-input-tokens-batch carries a
// clean `inferenceType: "Input tokens"`, so only the usagetype tail whitelist
// keeps its half-price batch rate out of the candidate set.
func TestRunPricingSync_batchRowIsNotAConflict(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"bedrock-apne1": {Type: "bedrock", Region: "ap-northeast-1"},
		},
		Models: map[string]config.ModelConfig{
			"nova": {Targets: []config.Target{{Provider: "bedrock-apne1", Model: "amazon.nova-pro-v1:0"}}},
		},
	}
	api := &fakePricingAPI{pages: map[string][][]string{
		"ap-northeast-1": {{docNovaInput, docNovaOutput, docNovaInputBatch, docNovaCacheRead}},
	}}
	var stdout, stderr bytes.Buffer
	if got := runPricingSync(&stdout, &stderr, cfg, api, ""); got != 0 {
		t.Fatalf("exit = %d, want 0 — the batch row must be dropped, not become a conflict\nstderr: %s", got, stderr.String())
	}
	rate := parseSyncFragment(t, stdout.Bytes()).Pricing.Overrides["bedrock-apne1"]["amazon.nova-pro-v1:0"]
	if got := string(rate.InputPerMTok); got != "0.96" {
		t.Errorf("input_per_mtok = %q, want \"0.96\" (0.48 is the BATCH rate — the tail whitelist has regressed)", got)
	}
}

func TestRunPricingSync_conflictingPricesAreUnresolved(t *testing.T) {
	// SYNTHETIC document: 0.0099 is a FAKE value injected into the real
	// `standard` row to force the two input SKUs to disagree — it is not a
	// published rate.
	conflicting := strings.Replace(docGLMInputStandard, "0.0012000000", "0.0099000000", 1)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"bedrock-apne1": {Type: "bedrock", Region: "ap-northeast-1"},
		},
		Models: map[string]config.ModelConfig{
			"glm": {Targets: []config.Target{{Provider: "bedrock-apne1", Model: "zai.glm-5"}}},
		},
	}
	api := &fakePricingAPI{pages: map[string][][]string{
		"ap-northeast-1": {{docGLMInput, docGLMOutput, conflicting}},
	}}
	var stdout, stderr bytes.Buffer
	if got := runPricingSync(&stdout, &stderr, cfg, api, ""); got != 1 {
		t.Fatalf("exit = %d, want 1 — disagreeing prices must not resolve", got)
	}
	for _, want := range []string{
		"conflicting",
		"APN1-zai.glm5-input-tokens",
		"APN1-zai.glm5-mantle-input-tokens-standard",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestRunPricingSync_crossRegionPrefixResolves(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"bedrock-apne1": {Type: "bedrock", Region: "ap-northeast-1"},
		},
		Models: map[string]config.ModelConfig{
			"glm": {Targets: []config.Target{{Provider: "bedrock-apne1", Model: "apac.zai.glm-5"}}},
		},
	}
	api := &fakePricingAPI{pages: map[string][][]string{"ap-northeast-1": {allApne1Docs()}}}
	var stdout, stderr bytes.Buffer
	if got := runPricingSync(&stdout, &stderr, cfg, api, ""); got != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", got, stderr.String())
	}
	overrides := parseSyncFragment(t, stdout.Bytes()).Pricing.Overrides["bedrock-apne1"]
	// The emitted key is the upstream id exactly as the config spells it — the
	// rate table is keyed on what the router actually sends upstream.
	if _, ok := overrides["apac.zai.glm-5"]; !ok {
		t.Errorf("fragment missing the configured key \"apac.zai.glm-5\":\n%s", stdout.String())
	}
	if _, ok := overrides["zai.glm-5"]; ok {
		t.Errorf("fragment keyed on the prefix-stripped id, want the configured one:\n%s", stdout.String())
	}
}

func TestRunPricingSync_apiErrorExitsTwo(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"bedrock-apne1": {Type: "bedrock", Region: "ap-northeast-1"},
		},
		Models: map[string]config.ModelConfig{
			"glm": {Targets: []config.Target{{Provider: "bedrock-apne1", Model: "zai.glm-5"}}},
		},
	}
	api := &fakePricingAPI{err: errors.New("AccessDeniedException: not authorized")}
	var stdout, stderr bytes.Buffer
	if got := runPricingSync(&stdout, &stderr, cfg, api, ""); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout not empty — no partial fragment on an API error:\n%s", stdout.String())
	}
}

// When nothing at all resolves, stdout must stay EMPTY. An
// `{"pricing":{"overrides":{}}}` on stdout reads as a successful no-op — a
// caller redirecting stdout to a file would end up with a syntactically valid
// pricing block that prices nothing, which is the same class of quiet failure
// the 0/0 override was. (Host-added guard: relaxing the `resolved > 0` emit
// condition survived every other test in this file.)
func TestRunPricingSync_nothingResolvedEmitsNoFragment(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"bedrock-use1": {Type: "bedrock", Region: "us-east-1"},
		},
		Models: map[string]config.ModelConfig{
			"claude": {Targets: []config.Target{{Provider: "bedrock-use1", Model: "anthropic.claude-sonnet-4-6"}}},
		},
	}
	api := &fakePricingAPI{pages: map[string][][]string{
		"us-east-1": {{docClaudeHaikuInput}},
	}}
	var stdout, stderr bytes.Buffer
	if got := runPricingSync(&stdout, &stderr, cfg, api, ""); got != 1 {
		t.Fatalf("exit = %d, want 1\nstderr: %s", got, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty — an overrides block that prices nothing must not be emitted", stdout.String())
	}
	if !strings.Contains(stderr.String(), "UNRESOLVED") {
		t.Errorf("stderr must still name the unresolved route:\n%s", stderr.String())
	}
}

func TestRunPricingSync_noBedrockProviderExitsTwo(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"selfhosted": {Type: "openai_compatible", BaseURL: "http://localhost:8000/v1"},
		},
		Models: map[string]config.ModelConfig{
			"local": {Targets: []config.Target{{Provider: "selfhosted", Model: "llama3.3"}}},
		},
	}
	api := &fakePricingAPI{}
	var stdout, stderr bytes.Buffer
	if got := runPricingSync(&stdout, &stderr, cfg, api, ""); got != 2 {
		t.Fatalf("exit = %d, want 2 — nothing to sync must not look like success", got)
	}
	if !strings.Contains(stderr.String(), "bedrock") {
		t.Errorf("stderr does not explain the missing bedrock providers:\n%s", stderr.String())
	}
}

func TestRunPricingSync_writesOutFile(t *testing.T) {
	newCfg := func() *config.Config {
		return &config.Config{
			Providers: map[string]config.ProviderConfig{
				"bedrock-apne1": {Type: "bedrock", Region: "ap-northeast-1"},
			},
			Models: map[string]config.ModelConfig{
				"glm": {Targets: []config.Target{{Provider: "bedrock-apne1", Model: "zai.glm-5"}}},
			},
		}
	}
	newAPI := func() *fakePricingAPI {
		return &fakePricingAPI{pages: map[string][][]string{"ap-northeast-1": {allApne1Docs()}}}
	}
	// First run to stdout, to capture the bytes the file must equal.
	var wantOut, stderr bytes.Buffer
	if got := runPricingSync(&wantOut, &stderr, newCfg(), newAPI(), ""); got != 0 {
		t.Fatalf("stdout run: exit = %d, want 0\nstderr: %s", got, stderr.String())
	}
	outPath := filepath.Join(t.TempDir(), "fragment.json")
	var stdout, stderr2 bytes.Buffer
	if got := runPricingSync(&stdout, &stderr2, newCfg(), newAPI(), outPath); got != 0 {
		t.Fatalf("file run: exit = %d, want 0\nstderr: %s", got, stderr2.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout not empty when --out is set:\n%s", stdout.String())
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, wantOut.Bytes()) {
		t.Errorf("file bytes differ from stdout bytes:\nfile:\n%s\nstdout:\n%s", b, wantOut.String())
	}
}

func TestFetchBedrockCandidates_paginates(t *testing.T) {
	api := &fakePricingAPI{pages: map[string][][]string{
		"ap-northeast-1": {{docGLMInput}, {docGLMOutput}},
	}}
	cands, err := fetchBedrockCandidates(context.Background(), api, "ap-northeast-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Errorf("candidates = %d, want 2 (one per page)", len(cands))
	}
	if api.calls != 2 {
		t.Errorf("calls = %d, want 2", api.calls)
	}
	for i, f := range api.gotFilters {
		if f != "regionCode=ap-northeast-1" {
			t.Errorf("filter[%d] = %q, want \"regionCode=ap-northeast-1\"", i, f)
		}
	}
}

func TestPriceCandidateFrom_dropsWhatItCannotRead(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"flex inferenceType", docGLMInputFlex},
		{"prompt cache read inferenceType", docNovaCacheRead},
		{"empty object", `{}`},
		{"not json", `not json`},
		{"unrecognized unit", strings.Replace(docGLMInput, `"unit":"1K tokens"`, `"unit":"Requests"`, 1)},
		{"unparseable USD", strings.Replace(docGLMInput, `"USD":"0.0012000000"`, `"USD":"n/a"`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := priceCandidateFrom(tc.doc); ok {
				t.Errorf("priceCandidateFrom accepted a document it cannot safely read")
			}
		})
	}
}
