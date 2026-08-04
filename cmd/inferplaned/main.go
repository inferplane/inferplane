// Command inferplaned is the inferplane control plane: policy and
// routing-rule distribution, budget-lease issuance, short-lived credential
// brokering, and telemetry aggregation. Inference traffic NEVER passes
// through it — mayu (cmd/mayu), the node-local data plane, sits on the
// request path and enforces what inferplaned distributes.
//
// Distribution (ADR-034) is live: --policies points at GovernancePolicy
// files/directories (watched — edits propagate on the next heartbeat), data
// planes POST /v1alpha1/sync (policy pull + consumption report + lease
// renewal + rejection report in one round trip), and GET /v1alpha1/dataplanes
// shows the connected version distribution. Credential brokering and
// telemetry aggregation are still to come (ADR-031).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/inferplane/inferplane/internal/controlplane"
	"github.com/inferplane/inferplane/internal/controlplane/ui"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/telemetry"
)

func main() {
	// Loopback-only by default: without INFERPLANED_TOKEN set there is no
	// authentication, so the control plane must not listen on all
	// interfaces. Set the token (shared with each mayu's
	// control_plane.token_ref) before widening the listen address.
	listen := flag.String("listen", "127.0.0.1:7601", "control plane listen address")
	policies := flag.String("policies", "", "GovernancePolicy file or directory to distribute (watched)")
	flag.Parse()

	if err := run(*listen, *policies, os.Getenv("INFERPLANED_TOKEN")); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// isLoopback reports whether the listen address binds only a loopback
// interface. An empty host (":7601") binds ALL interfaces and is not
// loopback; "localhost" is trusted as loopback without resolution.
func isLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func run(listen, policies, token string) error {
	// Refuse to serve unauthenticated beyond loopback (PR #50 review
	// finding): an open /v1alpha1/sync would let any network peer reset
	// reported spend or read the fleet view. An advisory log is not a
	// guard — this is.
	if token == "" && !isLoopback(listen) {
		return fmt.Errorf("INFERPLANED_TOKEN must be set when --listen (%s) is not loopback; refusing to start unauthenticated on a non-loopback address", listen)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The supported config API versions are the first thing a data
		// plane (or operator) needs to know before propagating rules.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersions": policy.SupportedAPIVersions,
		})
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Usage telemetry (ADR-036) mounts UNCONDITIONALLY — a telemetry-only
	// inferplaned (no --policies) is a valid deployment; policy distribution
	// below stays opt-in. Memory keeps a bounded 24h of windows; setting
	// INFERPLANED_USAGE_DSN (the INFERPLANED_TOKEN precedent — a fixed env
	// var, never a flag value carrying a secret) layers the durable Postgres
	// store behind it: writes ack only after the PG commit, queries fall
	// back to memory (marked degraded) through an outage. Construction is
	// lazy — a PG outage never blocks boot.
	agg := telemetry.Aggregator(telemetry.NewMemoryAggregator(24 * time.Hour))
	if dsn := os.Getenv("INFERPLANED_USAGE_DSN"); dsn != "" {
		pg, err := telemetry.NewPostgresAggregator(dsn)
		if err != nil {
			return fmt.Errorf("usage store: %w", err)
		}
		defer pg.Close()
		agg = telemetry.NewDurableAggregator(agg, pg)
		log.Print("inferplaned: usage telemetry persisting to postgres (INFERPLANED_USAGE_DSN set)")
	}
	controlplane.NewUsageServer(token, agg).Mount(mux)
	// The read-only usage console (data-free static shell; data via the
	// bearer-gated usage API — see internal/controlplane/ui).
	mux.Handle("/ui/", http.StripPrefix("/ui", ui.Handler()))

	var cp *controlplane.Server
	if policies != "" {
		var err error
		cp, err = controlplane.NewServer(token, policies)
		if err != nil {
			return fmt.Errorf("policies: %w", err)
		}
		cp.Mount(mux)
		go cp.Watch(ctx, func(err error) { log.Print("inferplaned: ", err) })
	}

	srv := &http.Server{
		Addr:    listen,
		Handler: mux,
		// Full read/write deadlines, not just headers: a slow-dripped sync
		// body would otherwise hold a goroutine forever — MaxBytesReader
		// bounds the size but not the rate (PR #50 review finding).
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		mode := "scaffold: health endpoints only"
		if cp != nil {
			mode = "distributing policies from " + policies
			if token == "" {
				mode += " (UNAUTHENTICATED — set INFERPLANED_TOKEN before leaving loopback)"
			}
		}
		log.Printf("inferplaned control plane listening on %s (%s)", listen, mode)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen on %s: %w", listen, err)
	case <-ctx.Done():
		log.Print("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
