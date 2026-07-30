// Package telemetry is the consolidation target for OTel instrumentation
// shared by both binaries (ADR-031). Today tracing lives in internal/tracing
// (OTLP exporter seam, ADR-011) and metrics in internal/metrics (Prometheus
// + GenAI semconv collectors); they migrate here as the control plane grows
// its aggregation side, so mayu and inferplaned emit consistently-named
// signals from one package. New cross-binary instrumentation should land
// here, not grow the legacy packages.
package telemetry
