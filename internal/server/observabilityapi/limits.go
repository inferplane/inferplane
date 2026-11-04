package observabilityapi

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

const maxLimitsBody = 64 << 10

// installLimits installs limits on the set; a seam so a test can fail the
// install after the state file was written.
var installLimits = func(s *otelexport.Set, l otelexport.Limits) error {
	_, err := s.SetLimits(l)
	return err
}

type limitsRequest struct {
	DryRun           bool              `json:"dry_run"`
	ExpectedRevision string            `json:"expected_revision"`
	Exporters        otelexport.Limits `json:"exporters"`
}

// limitChange is one changed field of one exporter in effective terms: a
// bootstrap default is shown as its value, never as an empty field.
type limitChange struct {
	Exporter string `json:"exporter"`
	Field    string `json:"field"` // paused | signals | max_content_bytes | max_sample_percent
	Before   any    `json:"before"`
	After    any    `json:"after"`
}

type targetState struct {
	Exported      bool `json:"exported"`
	SamplePercent int  `json:"sample_percent,omitempty"`
}

// limitEffect is one policy selection whose export the edit changes.
type limitEffect struct {
	Policy   string      `json:"policy"`
	Rule     string      `json:"rule"`
	Signal   string      `json:"signal"`
	Exporter string      `json:"exporter"`
	Before   targetState `json:"before"`
	After    targetState `json:"after"`
}

type limitsResult struct {
	DryRun          bool          `json:"dry_run"`
	CurrentRevision string        `json:"current_revision"`
	ResultRevision  string        `json:"result_revision"`
	Changes         []limitChange `json:"changes"`
	Effects         []limitEffect `json:"effects"`
}

// putLimits is PUT /admin/observability/limits: it replaces the runtime
// limits — a dry run reports the change, an apply persists, installs and
// audits it.
func (c *Console) putLimits(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.limitsState(); !state.Editable {
		writeError(w, http.StatusConflict, apiError{Error: "runtime limits are read-only on this gateway", Code: "read_only", Reason: state.Reason})
		return
	}
	var req limitsRequest
	if !decode(w, r, maxLimitsBody, &req) {
		return
	}
	set := c.opts.Set
	before := set.Limits()
	beforeBytes := otelexport.EncodeLimits(before)
	current := otelexport.Revision(beforeBytes)
	if !checkRevision(w, req.DryRun, req.ExpectedRevision, current) || !c.stateFileMatches(w, beforeBytes) {
		return
	}
	after, err := set.Normalize(req.Exporters)
	if err != nil {
		writeError(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "invalid_limits"})
		return
	}
	afterBytes := otelexport.EncodeLimits(after)
	res := limitsResult{DryRun: req.DryRun, CurrentRevision: current, ResultRevision: otelexport.Revision(afterBytes),
		Changes: c.limitChanges(before, after), Effects: c.limitEffects(before, after)}
	if req.DryRun || res.ResultRevision == current {
		writeJSON(w, http.StatusOK, res)
		return
	}
	if !c.auditReady(w) {
		return
	}
	// stateFileMatches established that the file (or its absence) equals the
	// installed limits; keep those bytes so a failed install can restore them.
	prior, readErr := os.ReadFile(c.opts.StateFile)
	existed := readErr == nil
	if err := writeFileAtomic(c.opts.StateFile, afterBytes); err != nil {
		fmt.Fprintln(os.Stderr, "inferplane: observability console: persist limits:", err)
		writeError(w, http.StatusInternalServerError, apiError{Error: "the limits could not be saved; nothing changed", Code: "persist_failed"})
		return
	}
	if err := installLimits(set, after); err != nil {
		fmt.Fprintln(os.Stderr, "inferplane: observability console: install limits:", err)
		var rerr error
		if existed {
			rerr = writeFileAtomic(c.opts.StateFile, prior)
		} else {
			rerr = os.Remove(c.opts.StateFile)
		}
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "inferplane: observability console: restore state file:", rerr)
		}
		writeError(w, http.StatusInternalServerError, apiError{Error: "the limits could not be installed; nothing changed", Code: "persist_failed"})
		return
	}
	c.emitChange(r, "observability_limits", current, res.ResultRevision, changedExporters(res.Changes))
	writeJSON(w, http.StatusOK, res)
}

// stateFileMatches refuses an edit when the state file no longer holds the
// installed limits (edited by hand while mayu runs): overwriting it would
// discard that edit silently. A restart loads the file.
func (c *Console) stateFileMatches(w http.ResponseWriter, installed []byte) bool {
	b, err := os.ReadFile(c.opts.StateFile)
	var disk []byte
	switch {
	case errors.Is(err, fs.ErrNotExist):
		disk = otelexport.EncodeLimits(nil)
	case err != nil:
		writeError(w, http.StatusInternalServerError, apiError{Error: "the state file could not be read", Code: "read_failed"})
		return false
	default:
		if l, derr := otelexport.DecodeLimits(b); derr == nil {
			clamped, _ := c.opts.Set.Clamp(l)
			if n, nerr := c.opts.Set.Normalize(clamped); nerr == nil {
				disk = otelexport.EncodeLimits(n)
			}
		}
	}
	if !bytes.Equal(disk, installed) {
		writeError(w, http.StatusConflict, apiError{Error: "the state file changed on disk after mayu loaded it; restart mayu to load it, or restore it", Code: "limits_file_changed"})
		return false
	}
	return true
}

// limitChanges lists every effective field that differs, per declared
// exporter in name order.
func (c *Console) limitChanges(before, after otelexport.Limits) []limitChange {
	out := []limitChange{}
	for _, e := range c.opts.Set.Status().Exporters {
		b, a := before[e.Name], after[e.Name]
		if b.Paused != a.Paused {
			out = append(out, limitChange{Exporter: e.Name, Field: "paused", Before: b.Paused, After: a.Paused})
		}
		if bs, as := effectiveSignals(e.Signals, b), effectiveSignals(e.Signals, a); strings.Join(bs, ",") != strings.Join(as, ",") {
			out = append(out, limitChange{Exporter: e.Name, Field: "signals", Before: bs, After: as})
		}
		if e.MaxContentBytes > 0 {
			if bc, ac := orDefault(b.MaxContentBytes, e.MaxContentBytes), orDefault(a.MaxContentBytes, e.MaxContentBytes); bc != ac {
				out = append(out, limitChange{Exporter: e.Name, Field: "max_content_bytes", Before: bc, After: ac})
			}
		}
		if hasSignal(e.Signals, policy.SignalTraces) {
			if bp, ap := orDefault(int64(b.MaxSamplePercent), 100), orDefault(int64(a.MaxSamplePercent), 100); bp != ap {
				out = append(out, limitChange{Exporter: e.Name, Field: "max_sample_percent", Before: bp, After: ap})
			}
		}
	}
	return out
}

// limitEffects lists every policy selection whose export or sampling the edit
// changes.
func (c *Console) limitEffects(before, after otelexport.Limits) []limitEffect {
	out := []limitEffect{}
	if c.opts.Policies == nil {
		return out
	}
	for _, p := range c.opts.Policies.Policies() {
		for _, r := range p.Rules {
			if r.Observability == nil {
				continue
			}
			bt, at := c.targets(before, r.Observability), c.targets(after, r.Observability)
			for i := range bt {
				bs, as := stateOf(bt[i]), stateOf(at[i])
				if bs != as {
					out = append(out, limitEffect{Policy: p.Name, Rule: r.Name, Signal: bt[i].Signal, Exporter: bt[i].Exporter, Before: bs, After: as})
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Policy != out[j].Policy {
			return out[i].Policy < out[j].Policy
		}
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Signal < out[j].Signal
	})
	return out
}

func stateOf(t Target) targetState {
	s := targetState{Exported: t.Exported}
	if t.Exported && t.Signal == policy.SignalTraces {
		s.SamplePercent = t.EffectivePercent
	}
	return s
}

func effectiveSignals(declared []string, l otelexport.Limit) []string {
	if l.Signals != nil {
		return append([]string(nil), l.Signals...)
	}
	return append([]string(nil), declared...)
}

func hasSignal(signals []string, signal string) bool {
	for _, s := range signals {
		if s == signal {
			return true
		}
	}
	return false
}

func orDefault(v, def int64) int64 {
	if v == 0 {
		return def
	}
	return v
}

func changedExporters(changes []limitChange) []string {
	seen := map[string]bool{}
	var out []string
	for _, ch := range changes {
		if !seen[ch.Exporter] {
			seen[ch.Exporter] = true
			out = append(out, ch.Exporter)
		}
	}
	sort.Strings(out)
	return out
}

// LoadLimits reads observability.console.state_file at boot and installs it
// on set. A missing file means no limits. A file that does not parse refuses
// boot: silently dropping a pause would resume export after a restart.
// Entries the current bootstrap no longer supports are narrowed (Set.Clamp)
// and returned as notes for the boot log.
func LoadLimits(path string, set *otelexport.Set) ([]string, error) {
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("observability.console.state_file: directory %s must exist", filepath.Dir(path))
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("observability.console.state_file: %w", err)
	}
	l, err := otelexport.DecodeLimits(b)
	if err != nil {
		return nil, fmt.Errorf("observability.console.state_file %s: %w (fix or remove the file; mayu does not start with a limit it cannot read)", path, err)
	}
	clamped, notes := set.Clamp(l)
	if _, err := set.SetLimits(clamped); err != nil {
		return nil, fmt.Errorf("observability.console.state_file %s: %w", path, err)
	}
	return notes, nil
}
