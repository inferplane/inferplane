package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
)

// pricingCmd implements `mayu pricing check` — the CI guard for ADR-030.
//
// It answers one question: does every model this config routes to have a rate?
// A newly released model added to `models` without a matching
// `pricing.overrides` entry would otherwise be billed 0 µUSD, and (with the
// default `on_missing: "allow"`) only a log line would say so. Wiring this into
// CI turns that into a build failure at the commit that introduced it.
//
// It reuses live.UnpricedTargets, the same function boot validation calls, so
// the check can never report something different from what the gateway would
// actually do. That shared path is why it goes through the real config loader
// rather than a lighter bespoke parse — a second parser would be free to drift
// from what the gateway enforces, which is the whole class of bug ADR-030 is
// about.
//
// Consequence for CI: the loader still resolves admin-token refs, so
// INFERPLANE_ADMIN_TOKEN must be set. Any non-empty value works — the lint
// never uses it.
//
// Exit codes: 0 all routes priced, 1 one or more unpriced, 2 config error.
func pricingCmd(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mayu pricing check --config <path>")
		return 2
	}
	switch args[0] {
	case "check":
		return pricingCheck(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown pricing subcommand %q\n", args[0])
		return 2
	}
}

func pricingCheck(args []string) int {
	fs := flag.NewFlagSet("pricing check", flag.ContinueOnError)
	cfgPath := fs.String("config", "config.json", "path to the gateway config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// LoadRaw, not Load: this is a lint over the declared topology, and it must
	// run in CI without production secrets on hand. Secret refs are irrelevant
	// to whether a route has a rate.
	cfg, err := config.LoadRaw(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	// Report on the same table the gateway would build, so a rate satisfied by
	// the cache derivation or the Bedrock region-prefix fallback counts as
	// present here too.
	unpriced := live.UnpricedTargets(cfg, live.PricingTableFor(cfg))
	if len(unpriced) == 0 {
		fmt.Printf("pricing: all %d configured model(s) have rates\n", len(cfg.Models))
		return 0
	}
	fmt.Fprintf(os.Stderr, "pricing: %d configured route(s) have NO rate and would be billed 0 uUSD:\n", len(unpriced))
	for _, pair := range unpriced {
		fmt.Fprintf(os.Stderr, "  %s\n", pair)
	}
	fmt.Fprintln(os.Stderr, "declare them under pricing.overrides with input_per_mtok + output_per_mtok (cache rates derive automatically)")
	return 1
}
