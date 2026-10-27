// Package inspectionfixture provides account-free synthetic benchmark inputs.
// It is imported only by tests; it never reads prompts or credentials.
package inspectionfixture

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var Protocols = []string{"anthropic", "openai", "responses", "bedrock", "bedrock-converse"}
var Sizes = []int{20 << 10, 200 << 10, 1 << 20, 8 << 20}
var Concurrency = []int{1, 8, 32}

// Request constructs exactly size bytes; all payload text is synthetic ASCII.
func Request(protocol string, size int) []byte {
	var prefix, suffix string
	switch protocol {
	case "anthropic", "bedrock":
		prefix = `{"model":"premium","messages":[{"role":"user","content":"`
		suffix = `"}],"max_tokens":32}`
	case "openai":
		prefix = `{"model":"premium","messages":[{"role":"user","content":"`
		suffix = `"}],"max_tokens":32}`
	case "responses":
		prefix = `{"model":"premium","input":"`
		suffix = `","max_output_tokens":32}`
	case "bedrock-converse":
		prefix = `{"messages":[{"role":"user","content":[{"text":"`
		suffix = `"}]}],"inferenceConfig":{"maxTokens":32}}`
	default:
		panic("unknown synthetic protocol")
	}
	n := size - len(prefix) - len(suffix)
	if n < 0 {
		panic("synthetic body size below envelope")
	}
	const text = "synthetic fixture text "
	return []byte(prefix + strings.Repeat(text, (n+len(text)-1)/len(text))[:n] + suffix)
}

// Run bounds aggregate logical request bytes to 16 MiB and records actual
// worker count. This cap also bounds independent decoded trees; payloads are
// immutable and shared. ns/op is throughput-derived; percentile metrics are
// per-operation latency. Setup and reporting are outside the measured region.
func Run(b *testing.B, bodyBytes, concurrency int, fn func() error) {
	b.Helper()
	workers := min(concurrency, max(1, (16<<20)/bodyBytes), max(1, b.N))
	var next atomic.Int64
	var failures atomic.Int64
	var first error
	var once sync.Once
	samples := make([][]int64, workers)
	stride := max(1, (b.N+workers*1024-1)/(workers*1024))
	var wg sync.WaitGroup
	b.ReportAllocs()
	b.ResetTimer()
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]int64, 0, 1024)
			for {
				i := next.Add(1) - 1
				if i >= int64(b.N) {
					break
				}
				start := time.Now()
				if err := fn(); err != nil {
					failures.Add(1)
					once.Do(func() { first = err })
				}
				if i%int64(stride) == 0 {
					local = append(local, time.Since(start).Nanoseconds())
				}
			}
			samples[w] = local
		}()
	}
	wg.Wait()
	b.StopTimer()
	all := make([]int64, 0, workers*1024)
	for _, values := range samples {
		all = append(all, values...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	for _, percentile := range []int{50, 95, 99} {
		if len(all) > 0 {
			b.ReportMetric(float64(all[(len(all)-1)*percentile/100]), fmt.Sprintf("p%d_ns", percentile))
		}
	}
	b.ReportMetric(float64(workers), "workers")
	b.ReportMetric(float64(failures.Load()), "failures")
	if status, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) > 1 {
					if kb, e := strconv.ParseInt(fields[1], 10, 64); e == nil {
						b.ReportMetric(float64(kb*1024), "rss_bytes")
					}
				}
			}
		}
	}
	if first != nil {
		b.Fatal(first)
	}
}
