package main

import (
	"fmt"
	"strings"

	"github.com/inferplane/inferplane/internal/policy"
)

// version is the build's version, embedded by the release pipeline:
//
//	go build -ldflags "-X main.version=v0.3.0" ./cmd/mayu
//
// "dev" (an un-stamped local build) reports upstream like any other value;
// a control plane with a configured fleet minimum judges it stale by
// design (roadmap ③ phase 1).
var version = "dev"

// versionCmd prints the build version and the policy API versions this
// build can enforce — the two axes the control plane's /v1alpha1/dataplanes
// view aggregates across the fleet.
func versionCmd(_ []string) error {
	fmt.Printf("mayu %s\napiVersions: %s\n", version, strings.Join(policy.SupportedAPIVersions, ", "))
	return nil
}
