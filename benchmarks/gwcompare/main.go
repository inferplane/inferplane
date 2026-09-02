// Command gwcompare is the reproducible head-to-head gateway benchmark
// behind docs/comparison.md §5 (Performance): the SAME OpenAI-compatible
// mock upstream, the SAME client, measured through inferplane's mayu,
// LiteLLM's proxy, Portkey's open-source gateway, and directly (the
// baseline every overhead number is relative to).
//
// Two subcommands:
//
//	gwcompare upstream -addr :9101 [-ttfb 0ms]
//	    Serves POST /v1/chat/completions (streaming + non-streaming) with a
//	    fixed, valid response. Zero artificial latency by default so the
//	    measured numbers are almost entirely GATEWAY overhead.
//
//	gwcompare bench -url http://127.0.0.1:8080/v1/chat/completions \
//	    -header 'Authorization: Bearer KEY' -model m -n 300 -c 1 [-stream]
//	    Runs n requests at concurrency c and prints mean/P50/P90/P99 of
//	    total latency (non-streaming) or time-to-first-content-chunk
//	    (streaming) in milliseconds, plus the error count.
//
// The mock's response body is identical across targets, so any latency
// difference IS the gateway. Numbers measure the proxy hop on loopback —
// they exclude network distance, which only widens the gap for any
// centrally-hosted gateway.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gwcompare upstream|bench [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "upstream":
		runUpstream(os.Args[2:])
	case "bench":
		runBench(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: gwcompare upstream|bench [flags]")
		os.Exit(2)
	}
}

const nonStreamBody = `{"id":"chatcmpl-bench","object":"chat.completion","created":1725000000,"model":"bench-model","choices":[{"index":0,"message":{"role":"assistant","content":"benchmark response body"},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":6,"total_tokens":31}}`

var streamChunks = []string{
	`{"id":"chatcmpl-bench","object":"chat.completion.chunk","created":1725000000,"model":"bench-model","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
	`{"id":"chatcmpl-bench","object":"chat.completion.chunk","created":1725000000,"model":"bench-model","choices":[{"index":0,"delta":{"content":"benchmark "},"finish_reason":null}]}`,
	`{"id":"chatcmpl-bench","object":"chat.completion.chunk","created":1725000000,"model":"bench-model","choices":[{"index":0,"delta":{"content":"response"},"finish_reason":null}]}`,
	`{"id":"chatcmpl-bench","object":"chat.completion.chunk","created":1725000000,"model":"bench-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":6,"total_tokens":31}}`,
}

func runUpstream(args []string) {
	fs := flag.NewFlagSet("upstream", flag.ExitOnError)
	addr := fs.String("addr", ":9101", "listen address")
	ttfb := fs.Duration("ttfb", 0, "artificial delay before the first byte")
	fs.Parse(args)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stream := bytes.Contains(body, []byte(`"stream":true`)) || bytes.Contains(body, []byte(`"stream": true`))
		if *ttfb > 0 {
			time.Sleep(*ttfb)
		}
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, nonStreamBody)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, c := range streamChunks {
			io.WriteString(w, "data: "+c+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
		io.WriteString(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	})
	// Some gateways probe /v1/models or /health on boot.
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"bench-model","object":"model"}]}`)
	})
	// Anthropic Messages endpoints, for driving REAL Anthropic-protocol
	// clients (Claude Code) through a gateway against this mock.
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stream := bytes.Contains(body, []byte(`"stream":true`)) || bytes.Contains(body, []byte(`"stream": true`))
		if *ttfb > 0 {
			time.Sleep(*ttfb)
		}
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_mock","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok: mock upstream reached through the gateway"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":9}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, ev := range []string{
			`event: message_start` + "\ndata: " + `{"type":"message_start","message":{"id":"msg_mock","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":1}}}`,
			`event: content_block_start` + "\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok: mock upstream reached through the gateway"}}`,
			`event: content_block_stop` + "\ndata: " + `{"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":9}}`,
			`event: message_stop` + "\ndata: " + `{"type":"message_stop"}`,
		} {
			io.WriteString(w, ev+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	})
	mux.HandleFunc("POST /v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":42}`)
	})
	fmt.Fprintln(os.Stderr, "mock upstream on", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type headerList []string

func (h *headerList) String() string     { return strings.Join(*h, ", ") }
func (h *headerList) Set(v string) error { *h = append(*h, v); return nil }

func runBench(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	url := fs.String("url", "", "chat completions URL")
	model := fs.String("model", "bench-model", "model name to request")
	n := fs.Int("n", 300, "requests")
	c := fs.Int("c", 1, "concurrency")
	warm := fs.Int("warmup", 20, "warmup requests (excluded)")
	stream := fs.Bool("stream", false, "measure streaming time-to-first-content-chunk")
	label := fs.String("label", "", "row label")
	var headers headerList
	fs.Var(&headers, "header", "extra header 'Name: value' (repeatable)")
	fs.Parse(args)
	if *url == "" {
		fmt.Fprintln(os.Stderr, "-url required")
		os.Exit(2)
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model": *model, "stream": *stream, "max_tokens": 64,
		"messages": []any{map[string]any{"role": "user", "content": "benchmark prompt: reply with the canned body"}},
	})
	client := &http.Client{Timeout: 30 * time.Second}
	one := func() (time.Duration, error) {
		req, _ := http.NewRequest(http.MethodPost, *url, bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		for _, h := range headers {
			k, v, ok := strings.Cut(h, ":")
			if ok {
				req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
			}
		}
		t0 := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return 0, fmt.Errorf("status %d: %s", resp.StatusCode, b)
		}
		if !*stream {
			io.Copy(io.Discard, resp.Body)
			return time.Since(t0), nil
		}
		// Streaming: latency = first CONTENT-bearing SSE chunk (the moment a
		// user sees text), then drain.
		br := bufio.NewReader(resp.Body)
		var ttfc time.Duration
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				break
			}
			if ttfc == 0 && strings.Contains(line, `"content":"`) {
				ttfc = time.Since(t0)
			}
		}
		if ttfc == 0 {
			return 0, fmt.Errorf("no content chunk seen")
		}
		return ttfc, nil
	}

	for i := 0; i < *warm; i++ {
		one()
	}
	lat := make([]time.Duration, 0, *n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := 0
	per := *n / *c
	for w := 0; w < *c; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				d, err := one()
				mu.Lock()
				if err != nil {
					if errs == 0 {
						fmt.Fprintln(os.Stderr, "error:", err)
					}
					errs++
				} else {
					lat = append(lat, d)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(lat) == 0 {
		fmt.Fprintln(os.Stderr, "all requests failed")
		os.Exit(1)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	q := func(p float64) float64 {
		return float64(lat[int(p*float64(len(lat)-1))].Microseconds()) / 1000
	}
	var sum time.Duration
	for _, d := range lat {
		sum += d
	}
	mean := float64(sum.Microseconds()) / float64(len(lat)) / 1000
	fmt.Printf("%s\tn=%d c=%d stream=%v\tmean=%.2fms p50=%.2fms p90=%.2fms p99=%.2fms errors=%d\n",
		*label, len(lat), *c, *stream, mean, q(0.50), q(0.90), q(0.99), errs)
}
