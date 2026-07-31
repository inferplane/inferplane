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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/inferplane/inferplane/internal/controlplane"
	"github.com/inferplane/inferplane/internal/policy"
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

func run(listen, policies, token string) error {
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
