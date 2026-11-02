// Command fixtures writes the request bodies of the tokenest live check
// (plan 2026-10-29 gateway path remediation, final task): every calibration
// class sized past a model window, the 2026-10-27 measured cases, and the
// dense overflow case in each ingress's wire shape. It never sends anything.
//
//	go run -buildvcs=false ./internal/tokenest/testdata/fixtures -out DIR
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/internal/tokenest/tokenesttest"
)

// bytesPerToken is each class's o200k_base rate (estimate_test.go realTokens
// over 200,000 bytes), used only to size bodies past a window.
var bytesPerToken = map[string]float64{
	"prose": 6.632, "code": 4.236, "read": 3.612, "digits": 3.324, "hex": 1.720, "hangul": 1.452, "korean": 4.335,
}

func main() {
	out := flag.String("out", "", "output directory")
	calib := flag.String("calib", "calib-gpt-oss", "model without a declared window (reaches Bedrock)")
	gated := flag.String("gated", "openai.gpt-oss-120b-1:0", "model with a declared 128k window")
	claude := flag.String("claude", "calib-haiku", "Claude model without a declared window")
	tokens := flag.Int("tokens", 260_000, "o200k tokens per calibration body (past a 131,072 window)")
	claudeTokens := flag.Int("claude-tokens", 400_000, "o200k tokens per Claude body (past a 200,000 window)")
	flag.Parse()
	if *out == "" {
		log.Fatal("-out is required")
	}
	estimates := new(bytes.Buffer)
	save := func(name string, body []byte, estimate int64) {
		if err := os.WriteFile(filepath.Join(*out, name+".json"), body, 0o600); err != nil {
			log.Fatal(err)
		}
		fmt.Fprintf(estimates, "%s\t%d\t%d\n", name, len(body), estimate)
	}
	write := func(name, protocol string, body []byte) { save(name, body, tokenest.Estimate(protocol, body)) }
	size := func(class string, target int) int { return int(float64(target) * bytesPerToken[class]) }
	for _, c := range tokenesttest.Classes {
		write("class-"+c.Name, "anthropic", tokenesttest.AnthropicBody(*calib, c.Text(1, size(c.Name, *tokens))))
	}
	for _, class := range []string{"prose", "korean"} {
		write("claude-"+class, "anthropic", tokenesttest.AnthropicBody(*claude, tokenesttest.ClassNamed(class).Text(1, size(class, *claudeTokens))))
	}
	prose := tokenesttest.ClassNamed("prose")
	write("measured-prose-624k", "anthropic", tokenesttest.AnthropicBody(*gated, prose.Text(2, 624_000)))
	write("measured-dense-484k", "anthropic", tokenesttest.AnthropicBody(*gated, tokenesttest.Dense(3, 484_000)))
	write("measured-dense-398k", "anthropic", tokenesttest.AnthropicBody(*gated, tokenesttest.Dense(3, 398_000)))
	write("bridge-prose-1000k", "responses", encode(map[string]any{
		"model": *gated, "store": false, "max_output_tokens": 16, "input": prose.Text(4, 1_000_000)}))

	dense := tokenesttest.Dense(3, 484_000)
	// The dense case passes the 128k gate by estimate and exceeds the real
	// 131,072-token window, so every ingress must relay Bedrock's refusal.
	write("overflow-messages", "anthropic", tokenesttest.AnthropicBody(*gated, dense))
	write("overflow-messages-stream", "anthropic", encode(map[string]any{"model": *gated, "max_tokens": 16, "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": dense}}}))
	write("overflow-chat", "openai", encode(map[string]any{"model": *gated, "max_tokens": 16,
		"messages": []any{map[string]any{"role": "user", "content": dense}}}))
	write("overflow-invoke", "bedrock", encode(map[string]any{"anthropic_version": "bedrock-2023-05-31", "max_tokens": 16,
		"messages": []any{map[string]any{"role": "user", "content": dense}}}))
	write("overflow-converse", "bedrock-converse", encode(map[string]any{"inferenceConfig": map[string]any{"maxTokens": 16},
		"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": dense}}}}}))
	write("overflow-responses", "responses", encode(map[string]any{"model": *gated, "store": false, "max_output_tokens": 16, "input": dense}))
	for _, name := range []string{"class-prose", "class-korean"} {
		body, err := os.ReadFile(filepath.Join(*out, name+".json"))
		if err != nil {
			log.Fatal(err)
		}
		// Bedrock CountTokens wrapper of the same body; the count endpoint
		// estimates the decoded inner body.
		save("count-"+name, encode(map[string]any{"input": map[string]any{"invokeModel": map[string]any{
			"body": base64.StdEncoding.EncodeToString(body)}}}), tokenest.Estimate("bedrock", body))
	}
	if err := os.WriteFile(filepath.Join(*out, "estimates.tsv"), estimates.Bytes(), 0o600); err != nil {
		log.Fatal(err)
	}
}

func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Fatal(err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
