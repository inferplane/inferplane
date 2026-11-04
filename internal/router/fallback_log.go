package router

import (
	"log"
	"sync"
)

// maxFallbackLogPairs bounds the distinct (requested, served) pairs logged per
// process; requested names are client input, so the set must not grow freely.
const maxFallbackLogPairs = 256

// maxFallbackLogName bounds how much of a client's model string is logged.
const maxFallbackLogName = 128

type fallbackLog struct {
	mu     sync.Mutex
	seen   map[[2]string]bool
	capped bool
}

// ObserveModelFallback reports a model fallback a generation ingress applied:
// the inferplane_model_fallback_total counter (labels: source and the
// configured served model only) and one log line per (requested, served)
// pair per process. The log carries no key, principal or prompt data; the
// requested name is truncated and quoted.
func (r *Router) ObserveModelFallback(requested, served, source string) {
	r.metrics.ObserveModelFallback(source, served)
	r.fallbackLog.note(requested, served, source)
}

func (l *fallbackLog) note(requested, served, source string) {
	if len(requested) > maxFallbackLogName {
		requested = requested[:maxFallbackLogName]
	}
	key := [2]string{requested, served}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen[key] {
		return
	}
	if len(l.seen) >= maxFallbackLogPairs {
		if !l.capped {
			l.capped = true
			log.Printf("router: model fallback logging capped at %d distinct requested models; inferplane_model_fallback_total keeps counting", maxFallbackLogPairs)
		}
		return
	}
	if l.seen == nil {
		l.seen = make(map[[2]string]bool)
	}
	l.seen[key] = true
	log.Printf("router: model fallback source=%s requested=%q served=%s", source, requested, served)
}
