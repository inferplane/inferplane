// Package cache will own mayu's node-local cache layer. It deliberately does
// NOT replace the provider's server-side prompt cache — server KV blocks
// cannot be built locally. The local layer has exactly three jobs (ADR-031):
//
//  1. deduplicating byte-identical requests;
//  2. cache-affinity routing — pinning a session/prefix to the same
//     region / inference profile so the SERVER cache stays warm (the most
//     important job; see v1alpha1.RoutingRule);
//  3. offline spooling while the control plane is unreachable.
//
// Storage is split by sensitivity, never mixed:
//
//   - prompt payloads and responses → VolatileStore (memory only): plaintext
//     prompts accumulating on the disks of hundreds of developer machines is
//     an instant audit finding, and loss on reboot is harmless because the
//     server cache TTL is far shorter anyway;
//   - lease state, usage counters, prefix hashes, routing-decision log →
//     SQLite, which must survive restarts and never holds a payload.
package cache

// VolatileStore holds prompt/response payloads in memory with no durable
// footprint. It is named for the guarantee (volatility), not a mechanism:
// macOS has neither tmpfs nor /dev/shm, so backends are chosen per platform
// (Linux may back it with tmpfs, macOS with in-process memory) behind this
// one interface.
type VolatileStore interface {
	// Get returns the payload stored under key, if any.
	Get(key string) (value []byte, ok bool)
	// Set stores value under key, replacing any previous payload.
	Set(key string, value []byte)
	// Delete removes key. Deleting an absent key is a no-op.
	Delete(key string)
}
