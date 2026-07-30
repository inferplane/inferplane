// Package proxy is the consolidation target for mayu's data-plane request
// path: model-specific schema translation, routing, and rule enforcement
// under budget leases (ADR-031). Today that logic lives in internal/server
// (ingress handlers), internal/router (resolution, fallback, breaker), and
// internal/openai (schema conversion); it migrates here incrementally as the
// data plane learns to take rules and leases from the control plane instead
// of a static config file. New request-path code should land here, not grow
// the legacy packages.
package proxy
