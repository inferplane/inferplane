// Command streaming measures the data-plane cost of putting mayu on a
// streamed Anthropic Messages request, against the two topologies it
// competes with: no gateway at all, and a central (network-hop) gateway.
//
// Three scenarios run against the same in-process mock upstream, which
// emits a fixed number of SSE content_block_delta events at a fixed
// cadence:
//
//	direct       client → upstream                       (baseline)
//	mayu         client → mayu (127.0.0.1) → upstream    (measured, real binary)
//	central-sim  client → delay proxy → upstream         (simulated network hop)
//
// mayu runs as a real subprocess from a generated config with a real
// virtual key, auditing and settling every request — the full hot path,
// not a stripped build. The central-hop simulation shifts the request and
// each response chunk by a configurable one-way transit latency with
// pipelining (chunk n+1 transits while n is in flight); it models network
// distance only, NOT the central gateway's own processing or queueing, so
// it is a lower bound on what a remote gateway adds. mayu's processing
// cost, by contrast, is genuinely measured.
//
// Run from the repository root:
//
//	go run ./benchmarks/streaming
//	go run ./benchmarks/streaming -requests 100 -chunks 60 -hop 8ms -json
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	requests := flag.Int("requests", 50, "measured requests per scenario")
	warmup := flag.Int("warmup", 5, "warm-up requests per scenario (unmeasured)")
	chunks := flag.Int("chunks", 40, "content_block_delta events per response")
	chunkInterval := flag.Duration("chunk-interval", 20*time.Millisecond, "upstream delay between SSE events (token cadence)")
	hop := flag.Duration("hop", 8*time.Millisecond, "one-way network transit latency for the central-gateway simulation")
	mayuPath := flag.String("mayu", "", "path to a prebuilt mayu binary (default: go build ./cmd/mayu into a temp dir)")
	jsonOut := flag.Bool("json", false, "emit results as JSON instead of a table")
	flag.Parse()

	if err := run(*requests, *warmup, *chunks, *chunkInterval, *hop, *mayuPath, *jsonOut); err != nil {
		fmt.Fprintln(os.Stderr, "benchmark:", err)
		os.Exit(1)
	}
}

func run(requests, warmup, chunks int, chunkInterval, hop time.Duration, mayuPath string, jsonOut bool) error {
	tmp, err := os.MkdirTemp("", "inferplane-bench-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	upstream, err := startUpstream(chunks, chunkInterval)
	if err != nil {
		return err
	}
	defer upstream.Close()

	proxy, err := startDelayProxy(upstream.URL(), hop)
	if err != nil {
		return err
	}
	defer proxy.Close()

	if mayuPath == "" {
		mayuPath = filepath.Join(tmp, "mayu")
		build := exec.Command("go", "build", "-trimpath", "-o", mayuPath, "./cmd/mayu")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			return fmt.Errorf("building mayu: %v\n%s", err, out)
		}
	}

	gw, err := startMayu(mayuPath, tmp, upstream.URL())
	if err != nil {
		return err
	}
	defer gw.Close()

	scenarios := []struct {
		name, url, key string
	}{
		{"direct", upstream.URL(), "bench-direct"},
		{"mayu", gw.url, gw.virtualKey},
		{"central-sim", proxy.URL(), "bench-direct"},
	}

	var results []scenarioResult
	for _, sc := range scenarios {
		r, err := measure(sc.name, sc.url, sc.key, requests, warmup, chunks)
		if err != nil {
			return fmt.Errorf("scenario %s: %w", sc.name, err)
		}
		results = append(results, r)
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report{
			Requests: requests, Chunks: chunks,
			ChunkIntervalMS: float64(chunkInterval) / float64(time.Millisecond),
			HopMS:           float64(hop) / float64(time.Millisecond),
			Scenarios:       results,
		})
	}
	printTable(results, requests, chunks, chunkInterval, hop)
	return nil
}

// --- mock Anthropic Messages upstream -------------------------------------

type upstreamServer struct {
	ln   net.Listener
	srv  *http.Server
	done chan struct{}
}

func startUpstream(chunks int, interval time.Duration) (*upstreamServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		emit := func(event, data string) {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
			fl.Flush()
		}
		emit("message_start", `{"type":"message_start","message":{"id":"msg_bench","type":"message","role":"assistant","model":"bench-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":1}}}`)
		emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		for i := 0; i < chunks; i++ {
			time.Sleep(interval)
			emit("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"tok "}}`)
		}
		emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
		emit("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":%d}}`, chunks))
		emit("message_stop", `{"type":"message_stop"}`)
	})
	s := &upstreamServer{ln: ln, srv: &http.Server{Handler: mux}, done: make(chan struct{})}
	go func() { s.srv.Serve(ln); close(s.done) }()
	return s, nil
}

func (s *upstreamServer) URL() string { return "http://" + s.ln.Addr().String() }
func (s *upstreamServer) Close() {
	s.srv.Shutdown(context.Background())
	<-s.done
}

// --- central-gateway network simulation ------------------------------------

// delayProxy forwards /v1/messages to the upstream, delaying the request by
// one hop and delivering each response chunk one hop after its arrival —
// pipelined, so inter-chunk cadence is preserved and only transit latency is
// added, exactly what a lossless network hop does. Gateway processing time
// and queueing are deliberately NOT modeled (lower bound).
type delayProxy struct {
	ln   net.Listener
	srv  *http.Server
	done chan struct{}
}

func startDelayProxy(upstream string, hop time.Duration) (*delayProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		time.Sleep(hop) // client → gateway transit
		req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		req.Header = r.Header.Clone()
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		fl := w.(http.Flusher)
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)

		type chunk struct {
			at   time.Time
			data []byte
		}
		ch := make(chan chunk, 1024)
		go func() {
			defer close(ch)
			buf := make([]byte, 32*1024)
			for {
				n, err := resp.Body.Read(buf)
				if n > 0 {
					ch <- chunk{at: time.Now(), data: append([]byte(nil), buf[:n]...)}
				}
				if err != nil {
					return
				}
			}
		}()
		for c := range ch {
			// gateway → client transit, pipelined against arrival time
			if d := time.Until(c.at.Add(hop)); d > 0 {
				time.Sleep(d)
			}
			w.Write(c.data)
			fl.Flush()
		}
	})
	p := &delayProxy{ln: ln, srv: &http.Server{Handler: mux}, done: make(chan struct{})}
	go func() { p.srv.Serve(ln); close(p.done) }()
	return p, nil
}

func (p *delayProxy) URL() string { return "http://" + p.ln.Addr().String() }
func (p *delayProxy) Close() {
	p.srv.Shutdown(context.Background())
	<-p.done
}

// --- mayu subprocess --------------------------------------------------------

type mayuProc struct {
	cmd        *exec.Cmd
	url        string
	virtualKey string
	logPath    string
}

func startMayu(binary, tmp, upstreamURL string) (*mayuProc, error) {
	dataPort, err := freePort()
	if err != nil {
		return nil, err
	}
	adminPort, err := freePort()
	if err != nil {
		return nil, err
	}
	keysDB := filepath.Join(tmp, "keys.db")

	env := append(os.Environ(),
		"INFERPLANE_ADMIN_TOKEN=bench-admin",
		"BENCH_UPSTREAM_KEY=bench-upstream",
	)

	mint := exec.Command(binary, "keys", "create", "--team", "bench", "--models", "*", "--store", keysDB)
	mint.Env = env
	out, err := mint.Output()
	if err != nil {
		return nil, fmt.Errorf("mayu keys create: %v", err)
	}
	virtualKey := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if !strings.HasPrefix(virtualKey, "ik_") {
		return nil, fmt.Errorf("unexpected keys create output: %q", string(out))
	}

	cfg := fmt.Sprintf(`{
  "server": {
    "listen": "127.0.0.1:%d",
    "admin_listen": "127.0.0.1:%d",
    "admin_auth": {"token_refs": [{"env": "INFERPLANE_ADMIN_TOKEN"}]}
  },
  "key_store": {"type": "sqlite", "path": %q},
  "audit": {"failure_mode": "buffer_then_block", "buffer": {"path": %q}, "sinks": [{"type": "file", "path": %q}]},
  "providers": {
    "bench-upstream": {"type": "anthropic", "base_url": %q, "api_key_ref": {"env": "BENCH_UPSTREAM_KEY"}}
  },
  "models": {"bench-model": {"targets": [{"provider": "bench-upstream", "model": "bench-model"}]}},
  "pricing": {
    "on_missing": "block",
    "version": "bench",
    "overrides": {"bench-upstream": {"bench-model": {"input_per_mtok": 3.0, "output_per_mtok": 15.0}}}
  }
}`, dataPort, adminPort, keysDB, filepath.Join(tmp, "audit.wal"), filepath.Join(tmp, "audit.jsonl"), upstreamURL)
	cfgPath := filepath.Join(tmp, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return nil, err
	}

	logPath := filepath.Join(tmp, "mayu.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, "serve", "--config", cfgPath)
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting mayu: %w", err)
	}
	p := &mayuProc{cmd: cmd, url: fmt.Sprintf("http://127.0.0.1:%d", dataPort), virtualKey: virtualKey, logPath: logPath}

	healthz := fmt.Sprintf("http://127.0.0.1:%d/healthz", adminPort)
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(healthz)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p, nil
			}
		}
		if time.Now().After(deadline) {
			p.Close()
			log, _ := os.ReadFile(logPath)
			return nil, fmt.Errorf("mayu did not become healthy in 15s; log:\n%s", log)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (p *mayuProc) Close() {
	if p.cmd.Process != nil {
		p.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { p.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			p.cmd.Process.Kill()
			<-done
		}
	}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// --- measurement ------------------------------------------------------------

type scenarioResult struct {
	Name         string  `json:"name"`
	TTFTP50MS    float64 `json:"ttft_p50_ms"`
	TTFTP99MS    float64 `json:"ttft_p99_ms"`
	InterChunkMS float64 `json:"inter_chunk_mean_ms"`
	TotalP50MS   float64 `json:"total_p50_ms"`
	TotalP99MS   float64 `json:"total_p99_ms"`
}

type report struct {
	Requests        int              `json:"requests"`
	Chunks          int              `json:"chunks"`
	ChunkIntervalMS float64          `json:"chunk_interval_ms"`
	HopMS           float64          `json:"hop_one_way_ms"`
	Scenarios       []scenarioResult `json:"scenarios"`
}

const requestBody = `{"model":"bench-model","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"benchmark"}]}`

func measure(name, baseURL, apiKey string, requests, warmup, wantChunks int) (scenarioResult, error) {
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	var ttfts, totals []float64
	var gapSum float64
	var gapN int

	for i := 0; i < warmup+requests; i++ {
		req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/messages", strings.NewReader(requestBody))
		if err != nil {
			return scenarioResult{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("x-api-key", apiKey)

		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return scenarioResult{}, err
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return scenarioResult{}, fmt.Errorf("status %d: %s", resp.StatusCode, body)
		}

		var ttft time.Duration
		var chunkTimes []time.Time
		gotChunks := 0
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if ttft == 0 {
				ttft = time.Since(start)
			}
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"content_block_delta"`) {
				gotChunks++
				chunkTimes = append(chunkTimes, time.Now())
			}
		}
		total := time.Since(start)
		resp.Body.Close()
		if err := sc.Err(); err != nil {
			return scenarioResult{}, fmt.Errorf("reading stream: %w", err)
		}
		if gotChunks != wantChunks {
			return scenarioResult{}, fmt.Errorf("got %d content_block_delta events, want %d", gotChunks, wantChunks)
		}
		if i < warmup {
			continue
		}
		ttfts = append(ttfts, ms(ttft))
		totals = append(totals, ms(total))
		for j := 1; j < len(chunkTimes); j++ {
			gapSum += ms(chunkTimes[j].Sub(chunkTimes[j-1]))
			gapN++
		}
	}

	return scenarioResult{
		Name:         name,
		TTFTP50MS:    percentile(ttfts, 0.50),
		TTFTP99MS:    percentile(ttfts, 0.99),
		InterChunkMS: gapSum / float64(gapN),
		TotalP50MS:   percentile(totals, 0.50),
		TotalP99MS:   percentile(totals, 0.99),
	}, nil
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func percentile(vals []float64, p float64) float64 {
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	idx := int(p * float64(len(s)-1))
	return s[idx]
}

func printTable(results []scenarioResult, requests, chunks int, interval, hop time.Duration) {
	fmt.Printf("streaming benchmark — %d requests/scenario, %d chunks @ %s cadence, central hop %s one-way (simulated transit only)\n\n",
		requests, chunks, interval, hop)
	fmt.Printf("%-12s %12s %12s %14s %12s %12s\n", "scenario", "TTFT p50", "TTFT p99", "inter-chunk", "total p50", "total p99")
	var direct *scenarioResult
	for i := range results {
		if results[i].Name == "direct" {
			direct = &results[i]
		}
	}
	for _, r := range results {
		fmt.Printf("%-12s %10.2fms %10.2fms %12.2fms %10.2fms %10.2fms\n",
			r.Name, r.TTFTP50MS, r.TTFTP99MS, r.InterChunkMS, r.TotalP50MS, r.TotalP99MS)
	}
	if direct != nil {
		fmt.Println()
		for _, r := range results {
			if r.Name == "direct" {
				continue
			}
			fmt.Printf("%-12s adds %+.2fms TTFT p50, %+.2fms total p50 vs direct\n",
				r.Name, r.TTFTP50MS-direct.TTFTP50MS, r.TotalP50MS-direct.TotalP50MS)
		}
	}
}
