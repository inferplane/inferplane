package controlplane

import (
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

// LedgerStore persists the lease ledger (roadmap ②, durability half): the
// per-(rule, dataplane) cumulative spent/allowance rows and each data
// plane's last-seen time. With a store attached, an inferplaned restart
// resumes grants exactly instead of re-learning from cumulative reports —
// and, the real correctness win, a data plane that died shortly before the
// restart keeps its reported spend on the books instead of dropping it.
//
// The interface mirrors bodystore's sqlite/postgres split posture: SQLite
// ships first (modernc, CGO stays off); a Postgres backend can land later
// without protocol changes. Writes are write-behind at heartbeat cadence
// (QPS = heartbeat-rate × planes — trivial), and a write failure must never
// fail the heartbeat: enforcement continues from memory, the store heals on
// the next write.
type LedgerStore interface {
	// Load returns everything persisted. Called once, at attach time.
	Load() ([]LedgerRow, []DataplaneRow, error)
	// SaveDataplane persists one heartbeat's effect: the data plane's
	// liveness row and its current spent/allowance across all rules, in one
	// transaction.
	SaveDataplane(dp DataplaneRow, rows []LedgerRow) error
	// DeleteDataplane removes a pruned data plane's rows (both tables).
	DeleteDataplane(id string) error
	Close() error
}

// LedgerRow is one (rule, dataplane) accounting row. Period is the budget
// window KIND the numbers were measured against — a row whose period no
// longer matches the rule's current period is stale currency and is skipped
// at Load, exactly like applyWire's carry-forward rule. WindowID (roadmap ②)
// is the window EPOCH ("2026-09"): a row from a previous epoch is equally
// stale — restoring it would resurrect a rolled-over window's spend — so
// Load skips it too. An empty WindowID (a row written by a pre-epoch build)
// restores under the period check alone, the meaning the row had when
// written.
type LedgerRow struct {
	Policy    string
	Rule      string
	Dataplane string
	Period    v1alpha1.BudgetPeriod
	WindowID  string
	Spent     int64
	Allowance int64
}

// DataplaneRow is a data plane's liveness row. Restoring LastSeen matters
// for accounting: a plane that was alive at shutdown may still hold a valid
// lease, and its outstanding allowance must count against the pool until
// the liveness horizon says otherwise.
type DataplaneRow struct {
	ID       string
	LastSeen time.Time
}
