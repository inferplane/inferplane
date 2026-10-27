package sensitivity

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func compareInspectionModes(t testing.TB, protocol string, raw []byte) {
	t.Helper()
	selective := NewInspector().(SelectiveInspector)
	before := bytes.Clone(raw)
	full, fullErr := selective.InspectWithMode(context.Background(), protocol, raw, DetectSensitiveData)
	structure, structureErr := selective.InspectWithMode(context.Background(), protocol, raw, StructureOnly)
	if !errors.Is(fullErr, structureErr) || !errors.Is(structureErr, fullErr) {
		t.Fatalf("mode errors differ: %v / %v", fullErr, structureErr)
	}
	if fullErr == nil && (!full.SensitiveDataInspected || structure.SensitiveDataInspected || len(structure.Categories) != 0) {
		t.Fatal("incorrect inspection evidence")
	}
	full.Categories = nil
	full.SensitiveDataInspected = false
	if !reflect.DeepEqual(full, structure) {
		t.Fatalf("structural results differ: %+v / %+v", full, structure)
	}
	if !bytes.Equal(raw, before) {
		t.Fatal("inspection modified input bytes")
	}
}

func TestInspectionModesPreserveStructure(t *testing.T) {
	for _, tt := range []struct{ name, protocol, raw string }{
		{"email", "anthropic", `{"messages":[{"role":"user","content":"person@example.test"}],"max_tokens":32}`},
		{"exact-number", "openai", `{"messages":[{"role":"user","content":"fixture"}],"metadata":{"card":4111111111111111,"large":9007199254740993},"max_tokens":32}`},
		{"encoded-tool", "openai", `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"type":"function","function":{"name":"fixture","arguments":"{\"nested\":{\"email\":\"person@example.test\",\"number\":9007199254740993}}"}}]}]}`},
		{"escaped", "responses", `{"input":"person\u0040example.test","max_output_tokens":32,"tools":[{"type":"function","name":"fixture","parameters":{"type":"object"}}]}`},
		{"stored-reference", "responses", `{"input":[{"type":"item_reference","id":"synthetic"}],"previous_response_id":"synthetic"}`},
		{"opaque", "anthropic", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YWJj"}}]}]}`},
		{"invoke", "bedrock", `{"messages":[{"role":"user","content":"fixture"}],"max_tokens":32}`},
		{"converse-unknown", "bedrock-converse", `{"messages":[{"role":"user","content":[{"text":"fixture"},{"future":{"data":"YWJj"}}]}],"inferenceConfig":{"maxTokens":32}}`},
	} {
		t.Run(tt.name, func(t *testing.T) { compareInspectionModes(t, tt.protocol, []byte(tt.raw)) })
	}
	full, err := NewInspector().Inspect(context.Background(), "openai", []byte(`{"messages":[{"role":"user","content":"fixture"}],"metadata":{"card":4111111111111111}}`))
	if err != nil || !slices.Contains(full.Categories, "credit_card") {
		t.Fatalf("full mode lost exact numeric detection: %+v, %v", full, err)
	}
}

func TestInspectionFullEvidence(t *testing.T) {
	selective := NewInspector().(SelectiveInspector)
	for _, raw := range []string{
		`{"messages":[{"role":"user","content":"synthetic"}]}`,
		`{"messages":[{"role":"user","content":"person@example.test"}]}`,
		`{"messages":[{"role":"user","content":[{"type":"future","data":"synthetic"}]}]}`,
	} {
		full, err := selective.InspectWithMode(context.Background(), "anthropic", []byte(raw), DetectSensitiveData)
		legacy, legacyErr := selective.Inspect(context.Background(), "anthropic", []byte(raw))
		if err != nil || legacyErr != nil || !full.SensitiveDataInspected || !reflect.DeepEqual(full, legacy) {
			t.Fatalf("full API contract mismatch: %+v, %+v, %v, %v", full, legacy, err, legacyErr)
		}
	}
}

func TestInspectionInvalidMode(t *testing.T) {
	result, err := NewInspector().(SelectiveInspector).InspectWithMode(context.Background(), "openai", []byte(`{"private":"synthetic-secret"}`), InspectionMode(255))
	if err == nil || strings.Contains(err.Error(), "synthetic-secret") || !reflect.DeepEqual(result, Result{}) {
		t.Fatalf("invalid mode must reject without request data: %+v, %v", result, err)
	}
}

func TestInspectionModesRejectMalformed(t *testing.T) {
	for _, raw := range []string{
		`{"messages":[],"messages":[{"role":"user","content":"fixture"}]}`,
		`{"messages":[{"role":"user","content":"{\"same\":1,\"same\":2}"}]}`,
		`{"messages":`,
		`{"messages":[]} {}`,
		`{"messages":[],"max_tokens":1.5}`,
		`{"messages":[],"max_tokens":-1}`,
		strings.Repeat("[", maxJSONDepth+2) + "0" + strings.Repeat("]", maxJSONDepth+2),
		"[" + strings.Repeat("0,", maxNodes) + "0]",
		"\"" + strings.Repeat("x", maxBodyBytes) + "\"",
	} {
		compareInspectionModes(t, "openai", []byte(raw))
		for _, mode := range []InspectionMode{DetectSensitiveData, StructureOnly} {
			if _, err := NewInspector().(SelectiveInspector).InspectWithMode(context.Background(), "openai", []byte(raw), mode); err == nil {
				t.Fatal("invalid input accepted")
			}
		}
	}
	encoded := `"fixture"`
	for range maxEncodedDepth + 2 {
		encoded = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(encoded) + `"`
	}
	compareInspectionModes(t, "openai", []byte(`{"messages":[{"role":"user","content":`+encoded+`}]}`))
	for _, mode := range []InspectionMode{DetectSensitiveData, StructureOnly} {
		if _, err := NewInspector().(SelectiveInspector).InspectWithMode(context.Background(), "openai", []byte(`{"messages":[{"role":"user","content":`+encoded+`}]}`), mode); !errors.Is(err, errLimit) {
			t.Fatalf("encoded depth bound lost: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result, err := NewInspector().(SelectiveInspector).InspectWithMode(ctx, "openai", []byte(`{"messages":[]}`), mode)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, Result{}) {
			t.Fatalf("cancellation: %+v, %v", result, err)
		}
		ctx, cancel = context.WithCancel(context.Background())
		during := &cancelDuringTraversal{Context: ctx, cancel: cancel}
		result, err = NewInspector().(SelectiveInspector).InspectWithMode(during, "openai", []byte(`{"messages":[{"role":"user","content":"`+strings.Repeat("synthetic ", 10000)+`"}]}`), mode)
		cancel()
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, Result{}) {
			t.Fatalf("cancellation during traversal: %+v, %v", result, err)
		}
	}
}
