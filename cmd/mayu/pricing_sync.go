package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"
	awspricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/pricing"
)

// pricingSync implements `mayu pricing sync` — an offline generator that reads
// the AWS Price List Query API and emits a `pricing.overrides` JSON fragment
// for the Bedrock routes a config declares.
//
// The five things an operator gets wrong, stated up front:
//
//  1. OFFLINE, operator-run. It is never called by `mayu serve`; the gateway
//     never fetches prices at request time (ADR-030 §6: audit-chain
//     tamper-evidence plus the no-external-SaaS-dependency constraint).
//  2. IAM: needs `pricing:GetProducts` (and `pricing:GetAttributeValues` if
//     extended). A missing permission is reported as a clear error and
//     nothing else — no retry, no fallback, no degraded mode.
//  3. The Price List Query API endpoint region is fixed (`us-east-1`) and
//     does not follow the Bedrock region being priced.
//  4. `type: openai_compatible` providers are skipped by design — a
//     self-hosted rate is deployment-specific (ADR-030 §5).
//  5. Exit codes: 0 all routes resolved · 1 one or more unresolved (the
//     fragment for the resolved ones is still printed) · 2 could not run
//     (usage, config, AWS API, or file write). It never emits a placeholder
//     or a 0 rate, because 0 means unpriced, not free.
func pricingSync(args []string) int {
	fs := flag.NewFlagSet("pricing sync", flag.ContinueOnError)
	cfgPath := fs.String("config", "config.json", "path to the gateway config")
	outPath := fs.String("out", "", "write the fragment to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// LoadRaw, not Load: this is an offline tool over the declared topology,
	// and it must run without production provider secrets on hand — the same
	// reason pricingCheck gives. Secret refs are irrelevant to what a route
	// costs. (LoadRaw still resolves admin-token refs, so
	// INFERPLANE_ADMIN_TOKEN must be set.)
	cfg, err := config.LoadRaw(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	// A bounded context, not Background: every Price List call this command makes
	// is a network round trip, and an unbounded one hangs the CLI forever.
	ctx, cancel := context.WithTimeout(context.Background(), pricingSyncTimeout)
	defer cancel()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: aws config: %v\n", err)
		return 2
	}
	api := awspricing.NewFromConfig(awsCfg, func(o *awspricing.Options) {
		o.Region = priceListEndpointRegion
	})
	return runPricingSync(os.Stdout, os.Stderr, cfg, api, *outPath)
}

// priceListEndpointRegion is where the Price List Query API itself lives. The
// SDK ships exactly three pricing endpoints (us-east-1, ap-south-1,
// eu-central-1) — there is no per-Bedrock-region pricing endpoint, so the
// client region is fixed here regardless of the Bedrock region being priced.
const priceListEndpointRegion = "us-east-1"

// pricingSyncTimeout bounds the whole run: credential resolution plus one
// paginated Price List fetch per region. Generous enough for a many-region
// config, finite so the command can never hang.
const pricingSyncTimeout = 5 * time.Minute

// pricingAPI is the narrow seam over aws-sdk-go-v2/service/pricing so the
// resolve/unresolved partition is unit-testable with no AWS credentials and no
// network — the same isolation internal/controlplane's stsAPI applies to STS
// and providers/bedrock applies to bedrockruntime. Satisfied by
// *awspricing.Client.
type pricingAPI interface {
	GetProducts(ctx context.Context, in *awspricing.GetProductsInput, optFns ...func(*awspricing.Options)) (*awspricing.GetProductsOutput, error)
}

// syncTarget is one (provider alias, region, upstream model id) triple a
// config routes to. Provider is the config ALIAS, which is the first half of
// the pricing.Key — never the provider TYPE.
type syncTarget struct {
	Provider string
	Region   string
	Model    string
}

// priceCandidate is one Price List product reduced to the three things that
// matter: which model family its usagetype names, which direction it prices,
// and the rate in USD per MILLION tokens.
type priceCandidate struct {
	ModelPart string   // normalized model segment of usagetype, e.g. "zaiglm5"
	Direction string   // "input" or "output"
	PerMTok   *big.Rat // exact; never float
	UsageType string   // raw, for error messages
}

// syncRate, syncOverrides and syncFragment shape the emitted JSON. The rates
// are json.RawMessage carrying ratToConfigNumber's exact decimal rendering —
// a float64 here would reintroduce the artifacts §R3 bans.
type syncRate struct {
	InputPerMTok  json.RawMessage `json:"input_per_mtok"`
	OutputPerMTok json.RawMessage `json:"output_per_mtok"`
}

type syncOverrides struct {
	Overrides map[string]map[string]syncRate `json:"overrides"`
}

type syncFragment struct {
	Pricing syncOverrides `json:"pricing"`
}

// normalizeID lowercases and strips every character that is not [a-z0-9], so
// the Price List's "zai.glm5" and a config's "zai.glm-5" compare equal. The
// two spell the same model with different punctuation; nothing else about them
// differs.
func normalizeID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// bedrockSyncTargets collects the config's bedrock routes, deduped on the
// whole triple and sorted by (Provider, Model) so the output is byte-stable
// across runs — Go map iteration is randomized, and a generator whose output
// reorders between runs is unusable in a diff-based workflow.
func bedrockSyncTargets(cfg *config.Config) []syncTarget {
	seen := map[syncTarget]struct{}{}
	var out []syncTarget
	for _, mc := range cfg.Models {
		for _, t := range mc.Targets {
			pc, ok := cfg.Providers[t.Provider]
			// Only type "bedrock" is priced from the AWS Price List.
			// `anthropic` and `openai_compatible` are excluded entirely: a
			// self-hosted or resold deployment's per-token rate is
			// deployment-specific, which is precisely ADR-030 §5's rationale
			// for the `allow` posture. No opt-in flag for them.
			if !ok || pc.Type != "bedrock" {
				continue
			}
			// An empty region is NOT a silent skip — it surfaces as an
			// unresolved target (§C8), because dropping a route quietly is
			// the failure mode this whole phase is about.
			st := syncTarget{Provider: t.Provider, Region: pc.Region, Model: t.Model}
			if _, dup := seen[st]; dup {
				continue
			}
			seen[st] = struct{}{}
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// priceListDoc is the subset of a Price List product document that
// priceCandidateFrom reads. Each PriceList element is a whole JSON document,
// not a struct — the SDK returns []string.
type priceListDoc struct {
	Product struct {
		Attributes struct {
			InferenceType string `json:"inferenceType"`
			UsageType     string `json:"usagetype"`
		} `json:"attributes"`
	} `json:"product"`
	Terms struct {
		OnDemand map[string]struct {
			PriceDimensions map[string]struct {
				Unit         string            `json:"unit"`
				PricePerUnit map[string]string `json:"pricePerUnit"`
			} `json:"priceDimensions"`
		} `json:"OnDemand"`
	} `json:"terms"`
}

// syncTailDirections is the usagetype tail whitelist. It is load-bearing and
// NOT redundant with the inferenceType gate: APN1-NovaPro-input-tokens-batch
// carries a clean `inferenceType: "Input tokens"` yet prices BATCH inference
// at half the on-demand rate — without this whitelist it becomes a conflicting
// candidate and every Nova model resolves to nothing. Same for -custom-model
// and -latency-optimized rows.
var syncTailDirections = map[string]string{
	"inputtokens":              "input",
	"inputtoken":               "input",
	"inputtokencount":          "input",
	"inputtokensstandard":      "input",
	"inputtokencountstandard":  "input",
	"outputtokens":             "output",
	"outputtoken":              "output",
	"outputtokencount":         "output",
	"outputtokensstandard":     "output",
	"outputtokencountstandard": "output",
}

// syncUnitMultipliers maps a normalized price unit to the factor that turns
// its USD figure into USD per million tokens. An unrecognized unit MUST drop
// rather than default to 1.
var syncUnitMultipliers = map[string]int64{
	"1ktokens":      1000,
	"1000tokens":    1000,
	"tokens":        1_000_000,
	"token":         1_000_000,
	"1mtokens":      1,
	"1mtoken":       1,
	"1000000tokens": 1,
}

// priceCandidateFrom reduces one Price List document to a priceCandidate.
// Every rule fails CLOSED: a document we cannot fully understand is dropped
// (ok == false), never guessed at.
func priceCandidateFrom(doc string) (priceCandidate, bool) {
	var d priceListDoc
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		return priceCandidate{}, false
	}
	// Direction gate on inferenceType: exactly "Input tokens" or "Output
	// tokens" (normalized). This excludes flex/priority/batch variants,
	// prompt-cache rows, and every embedding/image/audio row.
	var direction string
	switch normalizeID(d.Product.Attributes.InferenceType) {
	case "inputtokens":
		direction = "input"
	case "outputtokens":
		direction = "output"
	default:
		return priceCandidate{}, false
	}
	// usagetype always starts with a region abbreviation segment (APN1-,
	// USE1-). Cut at the first '-', normalize the rest, and split it at the
	// earliest "input"/"output" into model part and tail.
	usageType := d.Product.Attributes.UsageType
	dash := strings.Index(usageType, "-")
	if dash < 0 {
		return priceCandidate{}, false
	}
	rest := normalizeID(usageType[dash+1:])
	pos := strings.Index(rest, "input")
	if o := strings.Index(rest, "output"); o >= 0 && (pos < 0 || o < pos) {
		pos = o
	}
	if pos < 0 {
		return priceCandidate{}, false
	}
	modelPart, tail := rest[:pos], rest[pos:]
	// AWS's newer service-tier rows insert "mantle" before the direction
	// (APN1-zai.glm5-mantle-input-tokens-standard); it is not part of the
	// model id.
	modelPart = strings.TrimSuffix(modelPart, "mantle")
	if len(modelPart) < 4 {
		return priceCandidate{}, false // too generic to match safely
	}
	tailDir, ok := syncTailDirections[tail]
	if !ok || tailDir != direction {
		return priceCandidate{}, false
	}
	// Exactly one OnDemand offer with exactly one price dimension. More than
	// one means a tiered/ranged price we are not equipped to read.
	if len(d.Terms.OnDemand) != 1 {
		return priceCandidate{}, false
	}
	for _, offer := range d.Terms.OnDemand {
		if len(offer.PriceDimensions) != 1 {
			return priceCandidate{}, false
		}
		for _, dim := range offer.PriceDimensions {
			mult, ok := syncUnitMultipliers[normalizeID(dim.Unit)]
			if !ok {
				return priceCandidate{}, false
			}
			usd, ok := new(big.Rat).SetString(dim.PricePerUnit["USD"])
			if !ok {
				return priceCandidate{}, false
			}
			return priceCandidate{
				ModelPart: modelPart,
				Direction: direction,
				PerMTok:   usd.Mul(usd, new(big.Rat).SetInt64(mult)),
				UsageType: usageType,
			}, true
		}
	}
	return priceCandidate{}, false
}

// resolveTarget picks the input and output per-MTok rates for one model out of
// a region's candidate set. Returns a nil rate pair and a human reason when it
// cannot, which is the ONLY other outcome — it never guesses.
//
// Ambiguity is only a failure when the prices DISAGREE: GLM 5's on-demand row
// and its `standard` row are two distinct SKUs at the identical price, and
// requiring "exactly one candidate" would make the model unresolvable for no
// reason.
func resolveTarget(cands []priceCandidate, model string) (in, out *big.Rat, reason string) {
	// Match forms: the prefix-stripped id (pricing.BaseModelID uses the SAME
	// cross-region prefix list the rate lookup does), plus the id with its
	// vendor prefix dropped — the Price List keeps the vendor for some vendors
	// ("APN1-zai.glm5-…") and drops it for others ("APN1-NovaPro-…").
	base := pricing.BaseModelID(model)
	forms := []string{normalizeID(base)}
	if i := strings.Index(base, "."); i >= 0 {
		forms = append(forms, normalizeID(base[i+1:]))
	}
	// Tier 1: exact model-part hit. Tier 2: prefix hit in either direction
	// (absorbing the "-v1:0" version suffix a config carries and the Price
	// List does not). An exact hit must beat a prefix hit, so a more specific
	// row can never be diluted by a looser one.
	var tier1, tier2 []priceCandidate
	for _, c := range cands {
		exact, prefix := false, false
		for _, f := range forms {
			if c.ModelPart == f {
				exact = true
				break
			}
			// One direction only: the catalog row's model part must be a PREFIX
			// of the configured id, never the reverse. The reverse direction
			// let a shorter configured id match a longer, more specific
			// catalog row (a different model's rate) and bind it silently.
			if strings.HasPrefix(f, c.ModelPart) {
				prefix = true
			}
		}
		switch {
		case exact:
			tier1 = append(tier1, c)
		case prefix:
			tier2 = append(tier2, c)
		}
	}
	kept := tier1
	if len(kept) == 0 {
		kept = tier2
	}
	if len(kept) == 0 {
		return nil, nil, "no Price List SKU matched"
	}
	pick := func(direction string) (*big.Rat, string) {
		var distinct []*big.Rat
		var usageTypes []string
		for _, c := range kept {
			if c.Direction != direction {
				continue
			}
			usageTypes = append(usageTypes, c.UsageType)
			dup := false
			for _, v := range distinct {
				if v.Cmp(c.PerMTok) == 0 {
					dup = true
					break
				}
			}
			if !dup {
				distinct = append(distinct, c.PerMTok)
			}
		}
		switch {
		case len(distinct) == 0:
			return nil, fmt.Sprintf("no %s SKU matched", direction)
		case len(distinct) > 1:
			sort.Strings(usageTypes)
			return nil, fmt.Sprintf("%d conflicting %s prices (%s)", len(distinct), direction, strings.Join(usageTypes, ", "))
		}
		return distinct[0], ""
	}
	in, inReason := pick("input")
	out, outReason := pick("output")
	switch {
	case inReason != "" && outReason != "":
		return nil, nil, inReason + "; " + outReason
	case inReason != "":
		return nil, nil, inReason
	case outReason != "":
		return nil, nil, outReason
	}
	return in, out, ""
}

// ratToConfigNumber renders an exact rate as a JSON number with no float
// artifacts. Multiplying 0.0012 by 1000 in float64 does not give a clean 1.2,
// and a generated config full of 0.7999999999999999 is unreviewable — so the
// arithmetic is big.Rat throughout and only the final rendering is decimal.
// 12 places is far past anything published: AWS quotes 1K-token prices at 10
// decimals, which is at most 7 meaningful decimals per MTok.
func ratToConfigNumber(r *big.Rat) json.RawMessage {
	s := r.FloatString(12)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" {
		s = "0"
	}
	return json.RawMessage(s)
}

// fetchBedrockCandidates pulls one Bedrock region's whole Price List catalogue
// (a few hundred KB — VERIFIED 754 products in ap-northeast-1, 1032 in
// us-east-1) and reduces it to candidates. Fetched ONCE per region; every
// model resolves against the same slice.
func fetchBedrockCandidates(ctx context.Context, api pricingAPI, region string) ([]priceCandidate, error) {
	// 8 and 11 pages at MaxResults=100 are the VERIFIED real sizes; 200 is
	// 20x headroom, not a tuning knob. The cap exists so a server that always
	// returns a token cannot hang an operator's terminal forever.
	const maxPages = 200
	var cands []priceCandidate
	var next *string
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("still paginating after %d pages; refusing to loop forever", maxPages)
		}
		out, err := api.GetProducts(ctx, &awspricing.GetProductsInput{
			ServiceCode:   aws.String("AmazonBedrock"),
			FormatVersion: aws.String("aws_v1"),
			Filters: []awspricingtypes.Filter{{
				Type:  awspricingtypes.FilterTypeTermMatch,
				Field: aws.String("regionCode"),
				Value: aws.String(region),
			}},
			MaxResults: aws.Int32(100), // the API's maximum
			NextToken:  next,
		})
		if err != nil {
			return nil, fmt.Errorf("GetProducts: %w", err)
		}
		for _, doc := range out.PriceList {
			if c, ok := priceCandidateFrom(doc); ok {
				cands = append(cands, c)
			}
		}
		if out.NextToken == nil || *out.NextToken == "" {
			return cands, nil
		}
		next = out.NextToken
	}
}

// runPricingSync is the whole command except AWS client construction and
// config loading, so a test can drive it with a fake API and buffers.
func runPricingSync(stdout, stderr io.Writer, cfg *config.Config, api pricingAPI, outPath string) int {
	// Bounded, not Background: the per-region Price List fetches below are
	// network calls, and an unbounded context hangs the CLI indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), pricingSyncTimeout)
	defer cancel()
	targets := bedrockSyncTargets(cfg)
	if len(targets) == 0 {
		// Not 0: exiting 0 having produced nothing is indistinguishable from
		// success.
		fmt.Fprintln(stderr, `pricing sync: no providers of type "bedrock" in this config; nothing to sync`)
		return 2
	}
	byRegion := map[string][]syncTarget{}
	for _, t := range targets {
		byRegion[t.Region] = append(byRegion[t.Region], t)
	}
	regions := make([]string, 0, len(byRegion))
	for r := range byRegion {
		regions = append(regions, r)
	}
	sort.Strings(regions)
	type unresolvedTarget struct {
		t      syncTarget
		reason string
	}
	var unresolved []unresolvedTarget
	frag := syncFragment{Pricing: syncOverrides{Overrides: map[string]map[string]syncRate{}}}
	resolved := 0
	for _, region := range regions {
		if region == "" {
			// A bedrock provider with no region is reported, never silently
			// skipped — and never triggers a fetch.
			for _, t := range byRegion[region] {
				unresolved = append(unresolved, unresolvedTarget{t, "provider has no region configured"})
			}
			continue
		}
		cands, err := fetchBedrockCandidates(ctx, api, region)
		if err != nil {
			// Do not continue to other regions: a partial fragment silently
			// missing a region is worse than no fragment.
			fmt.Fprintf(stderr, "pricing sync: region %s: %v\n", region, err)
			return 2
		}
		for _, t := range byRegion[region] {
			in, out, reason := resolveTarget(cands, t.Model)
			if reason != "" {
				unresolved = append(unresolved, unresolvedTarget{t, reason})
				continue
			}
			m := frag.Pricing.Overrides[t.Provider]
			if m == nil {
				m = map[string]syncRate{}
				frag.Pricing.Overrides[t.Provider] = m
			}
			// Keyed on the upstream id exactly as the config spells it
			// (t.Model, not the prefix-stripped base): the rate table is
			// keyed on what the router actually sends upstream.
			m[t.Model] = syncRate{
				InputPerMTok:  ratToConfigNumber(in),
				OutputPerMTok: ratToConfigNumber(out),
			}
			resolved++
		}
	}
	// Emit only when something resolved: an empty {"pricing":{"overrides":{}}}
	// looks like a successful no-op and is exactly the kind of quiet output
	// this phase exists to remove.
	if resolved > 0 {
		b, err := json.MarshalIndent(frag, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "pricing sync: render fragment: %v\n", err)
			return 2
		}
		b = append(b, '\n')
		if outPath != "" {
			if err := os.WriteFile(outPath, b, 0o644); err != nil {
				fmt.Fprintf(stderr, "pricing sync: write %s: %v\n", outPath, err)
				return 2
			}
		} else if _, err := stdout.Write(b); err != nil {
			fmt.Fprintf(stderr, "pricing sync: write fragment: %v\n", err)
			return 2
		}
	}
	if len(unresolved) > 0 {
		fmt.Fprintf(stderr, "pricing sync: %d route(s) could not be resolved:\n", len(unresolved))
		for _, u := range unresolved {
			fmt.Fprintf(stderr, "pricing sync: UNRESOLVED %s/%s (region %s): %s\n", u.t.Provider, u.t.Model, u.t.Region, u.reason)
		}
		fmt.Fprintln(stderr, "no Price List SKU was found for the route(s) above — fill their rates in by hand from the AWS Bedrock pricing page; this tool will never emit a placeholder or a 0 rate (0 means unpriced, not free)")
		return 1
	}
	return 0
}
