package audit

// ChangeRef is the evidence of one admin configuration change, carried in
// RequestRef.Change on admin_observability_changed records. Target is a fixed
// name ("observability_limits" | "observability_rules"); Before and After are
// "sha256:<hex>" revisions of the persisted state (the exact bytes mayu
// wrote, so an auditor can hash the file and compare); Names lists the
// changed exporters or console rules. The state itself never enters the chain.
type ChangeRef struct {
	Target string   `json:"target"`
	Before string   `json:"before"`
	After  string   `json:"after"`
	Names  []string `json:"names,omitempty"`
}
