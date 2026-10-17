package sensitivity

import "context"

// Segments returns, in body order, every text the request inspector visits:
// object keys, string values, number spellings and the decoded pieces of
// encoded JSON strings (which therefore appear twice, encoded and decoded). It
// fails on exactly the inputs Inspect rejects. The strings are decoded copies;
// callers must not retain them beyond the request.
func Segments(ctx context.Context, protocol string, raw []byte) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch protocol {
	case "anthropic", "openai", "responses", "bedrock":
	default:
		return nil, errProtocol
	}
	var segments []string
	w := walker{ctx: ctx, visit: func(text string) error {
		segments = append(segments, text)
		return nil
	}}
	root, err := w.decode(raw, 0)
	if err != nil {
		return nil, err
	}
	var shape Result
	if err := shape.inspectShape(protocol, root); err != nil {
		return nil, err
	}
	if body, ok := root.(map[string]any); ok {
		if _, err := outputBudget(body); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return segments, nil
}
