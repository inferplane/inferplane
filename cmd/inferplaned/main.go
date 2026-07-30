// Command inferplaned is the inferplane control plane: policy and
// routing-rule distribution, budget-lease issuance, short-lived credential
// brokering, and telemetry aggregation. Inference traffic NEVER passes
// through it — mayu (cmd/mayu), the node-local data plane, sits on the
// request path and enforces what inferplaned distributes.
//
// This is a scaffold (ADR-031): it serves health/readiness only. It exists
// now — before it does anything useful — so that both binaries import
// internal/policy from day one and schema version skew is caught at compile
// time rather than after policy logic has grown into the proxy.
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

	"github.com/inferplane/inferplane/internal/policy"
)

func main() {
	// Loopback-only by default: the control plane has no authn yet, so it
	// must not listen on all interfaces until it does.
	listen := flag.String("listen", "127.0.0.1:7601", "control plane listen address")
	flag.Parse()

	if err := run(*listen); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(listen string) error {
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

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("inferplaned control plane listening on %s (scaffold: health endpoints only)", listen)
		errCh <- srv.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return fmt.Errorf("listen on %s: %w", listen, err)
	case sig := <-stop:
		log.Printf("received %s, shutting down", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
