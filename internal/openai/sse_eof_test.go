package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/pkg/schema"
)

const eofTextFrame = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"
const eofUsageFrame = "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\n"

func eofFinishFrame(reason string) string {
	return fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\n", reason)
}

type eofObservation struct {
	stops int
	raw   string
	usage *schema.Usage
	types []string
	err   error
}

func observeEOF(t *testing.T, r io.Reader) eofObservation {
	t.Helper()
	var got eofObservation
	var raw strings.Builder
	for ev, err := range ReadChatSSE(r, "public-model") {
		if err != nil {
			got.err = err
			continue
		}
		if ev == nil {
			continue
		}
		raw.Write(ev.Raw)
		if c := ev.Chunk; c != nil {
			got.types = append(got.types, c.Type)
			got.usage = schema.MergeUsage(got.usage, c.Usage)
			if c.Type == "message_stop" {
				got.stops++
				if len(ev.Raw) != 0 {
					t.Fatal("synthetic message_stop has native Raw bytes")
				}
			}
		}
	}
	got.raw = raw.String()
	return got
}

func TestReadChatSSECompleteEOFWithoutDone(t *testing.T) {
	for _, reason := range []string{"stop", "length", "tool_calls"} {
		t.Run(reason, func(t *testing.T) {
			start := eofTextFrame
			if reason == "tool_calls" {
				start = "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]}}]}\n\n"
			}
			body := start + eofFinishFrame(reason) + eofUsageFrame
			got := observeEOF(t, strings.NewReader(body))
			if got.err != nil || got.stops != 1 {
				t.Fatalf("complete EOF did not finish once: stops=%d err=%v types=%v", got.stops, got.err, got.types)
			}
			if got.raw != body || strings.Contains(got.raw, "[DONE]") {
				t.Fatal("EOF synthesis changed native output")
			}
			if got.usage == nil || *got.usage.InputTokens != 7 || *got.usage.OutputTokens != 3 {
				t.Fatalf("EOF synthesis lost usage: %+v", got.usage)
			}
			if got.types[len(got.types)-1] != "message_stop" {
				t.Fatalf("stop not terminal: %v", got.types)
			}
		})
	}
	t.Run("finish and explicit zero usage together", func(t *testing.T) {
		body := eofTextFrame + "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0}}\n\n"
		got := observeEOF(t, strings.NewReader(body))
		if got.stops != 1 || got.err != nil || got.raw != body {
			t.Fatalf("zero is complete known usage: %+v", got)
		}
	})
	t.Run("comments after usage", func(t *testing.T) {
		body := eofTextFrame + eofFinishFrame("stop") + eofUsageFrame
		got := observeEOF(t, strings.NewReader(body+": keepalive\n\n"))
		if got.stops != 1 || got.err != nil || got.raw != body {
			t.Fatalf("SSE comment changed completion: %+v", got)
		}
	})
}

func TestReadChatSSEEOFRequiresUsageAtOrAfterFinish(t *testing.T) {
	early := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"}}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":1}}\n\n"
	for _, tc := range []struct {
		name   string
		tail   string
		stops  int
		output int64
	}{
		{
			name: "early usage and further content without final usage",
			tail: eofFinishFrame("stop"), output: 1,
		},
		{
			name: "fresh usage after finish",
			tail: eofFinishFrame("stop") + eofUsageFrame, stops: 1, output: 3,
		},
		{
			name:  "fresh usage in finish frame",
			tail:  "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\n",
			stops: 1, output: 3,
		},
		{
			name: "existing DONE behavior with early usage",
			tail: eofFinishFrame("stop") + "data: [DONE]\n\n", stops: 1, output: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := early + eofTextFrame + tc.tail
			got := observeEOF(t, strings.NewReader(body))
			if got.err != nil || got.stops != tc.stops {
				t.Fatalf("stops=%d, want %d; err=%v", got.stops, tc.stops, got.err)
			}
			if got.raw != body {
				t.Fatal("terminal usage check changed native Raw")
			}
			if got.usage == nil || got.usage.InputTokens == nil || got.usage.OutputTokens == nil ||
				*got.usage.InputTokens != 7 || *got.usage.OutputTokens != tc.output {
				t.Fatalf("usage observations changed: %+v", got.usage)
			}
		})
	}
}

func TestReadChatSSEIncompleteEOFDoesNotFinish(t *testing.T) {
	complete := eofTextFrame + eofFinishFrame("stop") + eofUsageFrame
	cases := map[string]string{
		"empty":                           "",
		"text only":                       eofTextFrame,
		"missing usage":                   eofTextFrame + eofFinishFrame("stop"),
		"missing finish":                  eofTextFrame + eofUsageFrame,
		"unknown finish":                  eofTextFrame + eofFinishFrame("future_stop") + eofUsageFrame,
		"unrepresentable content filter":  eofTextFrame + eofFinishFrame("content_filter") + eofUsageFrame,
		"empty finish":                    eofTextFrame + eofFinishFrame("") + eofUsageFrame,
		"malformed finish":                eofTextFrame + "data: {\"choices\":[{\"finish_reason\":7}]}\n\n" + eofUsageFrame,
		"unknown finish after valid":      complete + eofFinishFrame("future_stop"),
		"malformed data after finish":     complete + "data: {broken}\n\n",
		"unmapped data after finish":      complete + "data: {\"error\":{\"message\":\"failed\"}}\n\n",
		"null data after finish":          complete + "data: null\n\n",
		"empty data after finish":         complete + "data:\n\n",
		"content after finish":            complete + eofTextFrame,
		"truncated post-finish data":      complete + "data: {\"choices\":",
		"partial final line":              strings.TrimRight(complete, "\n"),
		"missing final event delimiter":   strings.TrimSuffix(complete, "\n"),
		"unfinished SSE event after tail": complete + "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got := observeEOF(t, strings.NewReader(body))
			if got.stops != 0 {
				t.Fatalf("accepted incomplete EOF: %+v", got)
			}
			if strings.Contains(got.raw, "[DONE]") {
				t.Fatal("invented sentinel on incomplete EOF")
			}
		})
	}
}

func TestReadChatSSEEOFRequiresKnownNonnegativeUsage(t *testing.T) {
	for _, usage := range []string{
		`null`, `{}`, `{"total_tokens":10}`,
		`{"prompt_tokens":7}`, `{"completion_tokens":3}`,
		`{"prompt_tokens":null,"completion_tokens":3}`,
		`{"prompt_tokens":7,"completion_tokens":null}`,
		`{"prompt_tokens":-1,"completion_tokens":3}`,
		`{"prompt_tokens":7,"completion_tokens":-1}`,
		`{"prompt_tokens":7,"completion_tokens":3,"total_tokens":-1}`,
		`{"prompt_tokens":"unknown","completion_tokens":3}`,
		`{"prompt_tokens":7,"completion_tokens":1.5}`,
		`{"prompt_tokens":9223372036854775808,"completion_tokens":3}`,
		`{"prompt_tokens":7,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":-1}}`,
		`{"prompt_tokens":7,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":8}}`,
	} {
		t.Run(usage, func(t *testing.T) {
			body := eofTextFrame + eofFinishFrame("stop") + "data: {\"choices\":[],\"usage\":" + usage + "}\n\n"
			got := observeEOF(t, strings.NewReader(body))
			if got.stops != 0 {
				t.Fatalf("accepted unknown or invalid usage: %+v", got)
			}
		})
	}
	t.Run("negative counts cannot recover", func(t *testing.T) {
		body := eofTextFrame + eofFinishFrame("stop") +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":-1,\"completion_tokens\":3}}\n\n" + eofUsageFrame
		if got := observeEOF(t, strings.NewReader(body)); got.stops != 0 {
			t.Fatalf("later usage hid invalid count: %+v", got)
		}
	})
	t.Run("unknown counts cannot recover", func(t *testing.T) {
		body := eofTextFrame + eofFinishFrame("stop") +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":null,\"completion_tokens\":3}}\n\n" + eofUsageFrame
		if got := observeEOF(t, strings.NewReader(body)); got.stops != 0 {
			t.Fatalf("later usage hid unknown count: %+v", got)
		}
	})
}

type readFailure struct{ err error }

func (r readFailure) Read([]byte) (int, error) { return 0, r.err }

func TestReadChatSSEEOFTransportErrorDoesNotFinish(t *testing.T) {
	want := errors.New("upstream transport interrupted")
	body := eofTextFrame + eofFinishFrame("stop") + eofUsageFrame
	got := observeEOF(t, io.MultiReader(strings.NewReader(body), readFailure{want}))
	if !errors.Is(got.err, want) || got.stops != 0 || got.raw != body {
		t.Fatalf("transport failure became EOF completion: %+v", got)
	}
}

func TestReadChatSSEDoneStillStopsOnce(t *testing.T) {
	for _, body := range []string{
		eofTextFrame,
		eofTextFrame + eofFinishFrame("stop"),
		eofTextFrame + eofUsageFrame,
		eofTextFrame + eofFinishFrame("stop") + eofUsageFrame,
		eofTextFrame + eofFinishFrame("future_stop") + eofUsageFrame,
		eofTextFrame + "data: malformed\n\n",
	} {
		body += "data: [DONE]\n\n"
		got := observeEOF(t, strings.NewReader(body))
		if got.err != nil || got.stops != 1 || got.raw != body {
			t.Fatalf("changed existing sentinel behavior: %+v", got)
		}
	}
}

func TestReadChatSSEEOFConsumerCanStop(t *testing.T) {
	body := eofTextFrame + eofFinishFrame("stop") + eofUsageFrame
	for _, stopAt := range []string{"content_block_stop", "message_delta", "message_stop"} {
		t.Run(stopAt, func(t *testing.T) {
			found := false
			for ev, err := range ReadChatSSE(strings.NewReader(body), "public-model") {
				if err != nil {
					t.Fatal(err)
				}
				if ev.Chunk == nil {
					continue
				}
				if ev.Chunk.Type == stopAt {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("missing stop point %s", stopAt)
			}
		})
	}
}

func TestReadChatSSEEOFKeepsExplicitFinishReason(t *testing.T) {
	for reason, want := range map[string]string{"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use"} {
		var stop string
		for ev, err := range ReadChatSSE(strings.NewReader(eofTextFrame+eofFinishFrame(reason)+eofUsageFrame), "m") {
			if err != nil {
				t.Fatal(err)
			}
			if ev.Chunk != nil && ev.Chunk.Type == "message_delta" {
				var delta struct {
					StopReason string `json:"stop_reason"`
				}
				if json.Unmarshal(ev.Chunk.Delta, &delta) == nil && delta.StopReason != "" {
					stop = delta.StopReason
				}
			}
		}
		if stop != want {
			t.Fatalf("%s changed to %s, want %s", reason, stop, want)
		}
	}
}
