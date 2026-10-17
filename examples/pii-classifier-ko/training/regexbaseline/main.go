// Command regexbaseline scores neutral rows with the gateway's existing
// finite detectors (internal/sensitivity), as the baseline the learned
// classifiers are compared against. Each row is wrapped as one Anthropic
// user message, the shape Claude Code traffic has at the gateway.
//
// Usage: go run ./research/pii-finetune/regexbaseline < rows.jsonl > preds.jsonl
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/inferplane/inferplane/internal/sensitivity"
)

type row struct {
	ID         string   `json:"id"`
	Text       string   `json:"text"`
	Label      int      `json:"label"`
	Categories []string `json:"categories"`
	Genre      string   `json:"genre"`
	Kind       string   `json:"kind"`
	Lang       string   `json:"lang"`
	Source     string   `json:"source"`
}

func main() {
	in := sensitivity.NewInspector()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	for sc.Scan() {
		var r row
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			log.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{
			"model": "baseline", "max_tokens": 16,
			"messages": []map[string]any{{"role": "user", "content": r.Text}},
		})
		start := time.Now()
		res, err := in.Inspect(context.Background(), "anthropic", body)
		ms := float64(time.Since(start).Microseconds()) / 1000
		p := 0.0
		if err != nil || !res.Complete || len(res.Categories) > 0 {
			p = 1 // uninspectable fails closed, as onUninspectable would
		}
		if err := enc.Encode(map[string]any{
			"id": r.ID, "label": r.Label, "p": p, "ms": ms, "err": nil, "detected": res.Categories,
			"categories": r.Categories, "genre": r.Genre, "kind": r.Kind, "lang": r.Lang, "source": r.Source,
		}); err != nil {
			log.Fatal(err)
		}
	}
	if err := sc.Err(); err != nil {
		log.Fatal(err)
	}
}
