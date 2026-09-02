package controlplane

// Mutation audit for control-plane management writes (Phase 0b-4, design
// spec §4.4): every policy PUT/DELETE appends an admin_mutation record —
// actor (durable identity or "static-token"), capability, action, scope,
// sha256 of the canonical document before and after, and the resulting
// generation. Hashes, never bodies: policy documents are refs-only by
// schema, but hashing keeps records small and diffable against exports.
//
// The sink is an append-only JSONL writer (INFERPLANED_MUTATION_LOG); with
// none attached, records go to the process log so a mutation is NEVER
// silent — the ADR-038 accepted limitation this closes was "no mutation
// audit at all".

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"sync"
	"time"
)

// MutationRecord is one management write.
type MutationRecord struct {
	TS           string `json:"ts"`
	Event        string `json:"event"` // always "admin_mutation"
	Actor        string `json:"actor"` // issuer#sub | "static-token" | "" (unauthenticated loopback posture)
	Capability   string `json:"capability"`
	Action       string `json:"action"` // "put" | "delete"
	Scope        string `json:"scope"`  // e.g. the policy name
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
	Generation   string `json:"generation,omitempty"`
}

type mutationLog struct {
	mu sync.Mutex
	w  io.Writer // nil ⇒ process log fallback
}

// SetMutationLog attaches an append-only JSONL sink for admin_mutation
// records. nil keeps the process-log fallback.
func (s *Server) SetMutationLog(w io.Writer) {
	s.mutations.mu.Lock()
	s.mutations.w = w
	s.mutations.mu.Unlock()
}

// recordMutation appends one record. A sink write failure falls back to the
// process log — a mutation is never silent, and never fails the request
// that already succeeded.
func (s *Server) recordMutation(rec MutationRecord) {
	rec.TS = time.Now().UTC().Format(time.RFC3339Nano)
	rec.Event = "admin_mutation"
	line, err := json.Marshal(rec)
	if err != nil {
		log.Printf("inferplaned: admin_mutation (marshal failed): %+v", rec)
		return
	}
	s.mutations.mu.Lock()
	w := s.mutations.w
	if w != nil {
		_, err = w.Write(append(line, '\n'))
	}
	s.mutations.mu.Unlock()
	if w == nil || err != nil {
		log.Printf("inferplaned: %s", line)
	}
}

// sha256Hex returns the hex sha256 of b, or "" for empty input (an absent
// document hashes to nothing, not to the hash of the empty string —
// "before" on a create and "after" on a delete are absences).
func sha256Hex(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// canonicalPolicyJSON returns the canonical JSON of the named document in
// the CURRENT applied set (nil when absent) — the before/after hash input
// for policy mutation records.
func (s *Server) canonicalPolicyJSON(name string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.wire {
		if s.wire[i].Metadata.Name == name {
			b, _ := json.Marshal(&s.wire[i])
			return b
		}
	}
	return nil
}
