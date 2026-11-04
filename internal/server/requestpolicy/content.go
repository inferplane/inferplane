package requestpolicy

import "context"

// ContentRecord is one request's egress bytes for governed content export:
// the provider request after routing, masking and reinspection (the same bytes
// body capture stores, ADR-018) and the buffered response when one exists
// (nil for streams). RecordID is the audit record's id so the two join.
type ContentRecord struct {
	RecordID string
	Team     string
	Account  string // accounting subject, for the user-subject policy match
	Ingress  string
	Model    string
	Upstream string
	TraceID  string
	Request  []byte
	Response []byte
}

// ContentExport receives a ContentRecord after the response was written to
// the client. It must not block or retain ctx; the byte slices are shared with
// body capture and must not be modified.
type ContentExport func(ctx context.Context, c ContentRecord)

// Export calls f when it is set.
func (f ContentExport) Export(ctx context.Context, c ContentRecord) {
	if f != nil {
		f(ctx, c)
	}
}
