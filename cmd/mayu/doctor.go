package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/providers"
)

// doctorCmd implements `mayu doctor` (roadmap ④): one command that answers
// "why does it fail only on THIS machine" without grepping logs. Human
// output by default, `--json` for support tickets.
//
// Same discipline as `pricing check`: every check reuses the exact function
// the gateway itself runs (config.Load, live.BuildState, live.UnpricedTargets,
// policy.LoadWirePaths), so doctor can never diagnose something different
// from what serve would do. And the same leakage bar as /admin/config:
// secret REF names may appear, resolved values never — provider probe
// Details are already sanitized by contract (providers.HealthResult).
//
// Exit codes: 0 no failing check (warns allowed), 1 at least one failure,
// 2 usage error.
func doctorCmd(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	cfgPath := fs.String("config", "config.json", "path to the gateway config")
	jsonOut := fs.Bool("json", false, "emit the report as JSON (for support tickets)")
	noProbe := fs.Bool("no-probe", false, "skip provider connection probes (bedrock's probe makes a real 1-token call)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	rep := runDoctor(*cfgPath, *noProbe)

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		fmt.Printf("mayu doctor — %s (apiVersions: %s)\n\n", rep.Version, strings.Join(rep.APIVersions, ", "))
		for _, c := range rep.Checks {
			fmt.Printf("  %-4s  %-14s %s\n", c.Level, c.Name, c.Detail)
		}
	}
	for _, c := range rep.Checks {
		if c.Level == "fail" {
			return 1
		}
	}
	return 0
}

type doctorReport struct {
	Version     string        `json:"version"`
	APIVersions []string      `json:"apiVersions"`
	Checks      []doctorCheck `json:"checks"`
}

type doctorCheck struct {
	Name   string `json:"name"`
	Level  string `json:"level"` // "ok" | "warn" | "fail"
	Detail string `json:"detail"`
}

func runDoctor(cfgPath string, noProbe bool) *doctorReport {
	rep := &doctorReport{Version: version, APIVersions: policy.SupportedAPIVersions}
	add := func(name, level, detail string) { rep.Checks = append(rep.Checks, doctorCheck{name, level, detail}) }

	// 1. Parse/validate WITHOUT secret resolution, so a missing env var and
	// a malformed document diagnose differently.
	raw, err := config.LoadRaw(cfgPath)
	if err != nil {
		add("config", "fail", err.Error())
		return rep // nothing below is meaningful on an unparseable config
	}
	add("config", "ok", fmt.Sprintf("%s parses and validates (%d provider(s), %d model route(s))", cfgPath, len(raw.Providers), len(raw.Models)))

	// 2. Secret refs: the full loader resolves every ref; its errors name
	// the REF (env var name / file path), never a value.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		add("secrets", "fail", err.Error())
	} else {
		add("secrets", "ok", "all secret refs resolve")
	}

	// 3. Policy source (the two channels are mutually exclusive by config
	// validation; "none" is a legitimate standalone posture).
	switch {
	case len(raw.Policies) > 0:
		wire, files, perr := policy.LoadWirePaths(raw.Policies...)
		if perr != nil {
			add("policy-source", "fail", fmt.Sprintf("local files (%s): %v", strings.Join(raw.Policies, ", "), perr))
		} else {
			add("policy-source", "ok", fmt.Sprintf("local files: %d document(s) from %d file(s)", len(wire), len(files)))
		}
	case raw.ControlPlane != nil:
		add("policy-source", "ok", "control plane: "+raw.ControlPlane.URL)
	default:
		add("policy-source", "ok", "none (standalone: config RBAC + per-key limits only)")
	}

	// 4. Listen ports — informational either way: busy usually means the
	// gateway is already running here, free means it is not.
	for name, addr := range map[string]string{"data plane": raw.Server.Listen, "admin plane": raw.Server.AdminListen} {
		if addr == "" {
			continue
		}
		if ln, lerr := net.Listen("tcp", addr); lerr != nil {
			add("port", "ok", fmt.Sprintf("%s %s: in use (a gateway is likely already running)", name, addr))
		} else {
			ln.Close()
			add("port", "ok", fmt.Sprintf("%s %s: free (no gateway listening here)", name, addr))
		}
	}

	// 5. Pricing coverage — the ADR-030 zero-billing guard, same table the
	// gateway builds at boot.
	if unpriced := live.UnpricedTargets(raw, live.PricingTableFor(raw)); len(unpriced) > 0 {
		add("pricing", "fail", fmt.Sprintf("%d route(s) without a rate (would bill 0 µUSD): %s", len(unpriced), strings.Join(unpriced, ", ")))
	} else {
		add("pricing", "ok", fmt.Sprintf("all %d configured model(s) have rates", len(raw.Models)))
	}

	// 6. Providers: construct the same live state serve would, then probe
	// each provider that implements the ADR-014 HealthChecker. A broker-mode
	// bedrock provider constructs only with a serve-time credential fetcher,
	// so a construction error here is reported as a warn with that context,
	// not silently swallowed.
	if cfg != nil {
		state, _, berr := live.BuildState(cfg)
		var provs map[string]providers.Provider
		if berr == nil {
			provs = state.Providers()
		}
		switch {
		case berr != nil:
			add("providers", "warn", fmt.Sprintf("provider construction: %v (a broker-mode provider needs the serve-time credential fetcher; other errors here also fail `mayu serve`)", berr))
		case noProbe:
			add("providers", "ok", fmt.Sprintf("%d provider(s) constructed (probes skipped: --no-probe)", len(provs)))
		default:
			names := make([]string, 0, len(provs))
			for name := range provs {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				hc, ok := provs[name].(providers.HealthChecker)
				if !ok {
					add("provider", "ok", name+": probe unsupported")
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				res := hc.HealthCheck(ctx)
				cancel()
				level := "ok"
				if !res.OK {
					level = "fail"
				}
				add("provider", level, fmt.Sprintf("%s: %s (%dms)", name, res.Detail, res.LatencyMS))
			}
		}
	}

	// 7. Control plane: reachability + latency via /readyz (unauthenticated
	// by design), apiVersion overlap, then an authenticated read to prove
	// the token is accepted — never a sync POST, which would register this
	// doctor run as a data plane in the lease ledger.
	if cfg != nil && cfg.ControlPlane != nil {
		client := &http.Client{Timeout: 5 * time.Second}
		start := time.Now()
		resp, cerr := client.Get(cfg.ControlPlane.URL + "/readyz")
		if cerr != nil {
			add("control-plane", "fail", fmt.Sprintf("unreachable: %v", cerr))
		} else {
			var ready struct {
				APIVersions []string `json:"apiVersions"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&ready)
			resp.Body.Close()
			add("control-plane", "ok", fmt.Sprintf("reachable in %dms (server apiVersions: %s)", time.Since(start).Milliseconds(), strings.Join(ready.APIVersions, ", ")))
			if len(ready.APIVersions) > 0 && !versionsOverlap(ready.APIVersions, policy.SupportedAPIVersions) {
				add("control-plane", "fail", "no shared policy apiVersion with the control plane — every distributed document would be rejected")
			}
			req, _ := http.NewRequest(http.MethodGet, cfg.ControlPlane.URL+"/v1alpha1/dataplanes", nil)
			if cfg.ControlPlane.Token != "" {
				req.Header.Set("Authorization", "Bearer "+cfg.ControlPlane.Token)
			}
			aresp, aerr := client.Do(req)
			switch {
			case aerr != nil:
				add("control-plane", "fail", fmt.Sprintf("auth check: %v", aerr))
			case aresp.StatusCode == http.StatusOK:
				aresp.Body.Close()
				add("control-plane", "ok", "token accepted")
			case aresp.StatusCode == http.StatusUnauthorized || aresp.StatusCode == http.StatusForbidden:
				aresp.Body.Close()
				add("control-plane", "fail", fmt.Sprintf("token rejected (status %d) — check control_plane.token_ref", aresp.StatusCode))
			default:
				aresp.Body.Close()
				add("control-plane", "warn", fmt.Sprintf("auth check: unexpected status %d", aresp.StatusCode))
			}
		}
	}

	return rep
}

func versionsOverlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
