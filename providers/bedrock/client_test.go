package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/inferplane/inferplane/pkg/schema"
)

func TestBuildMessagesToolBlocks(t *testing.T) {
	text := "list files"
	input := json.RawMessage(`{"cmd":"ls"}`)
	errFlag := true
	msgs := []ConverseMessage{
		{Role: "user", Content: []schema.ContentBlock{{Type: "text", Text: &text}}},
		{Role: "assistant", Content: []schema.ContentBlock{{Type: "tool_use", ID: "t1", Name: "bash", Input: input}}},
		{Role: "user", Content: []schema.ContentBlock{{Type: "tool_result", ToolUseID: "t1", Content: json.RawMessage(`"a.go\nb.go"`), IsError: &errFlag}}},
	}
	out := buildMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	if _, ok := out[0].Content[0].(*brtypes.ContentBlockMemberText); !ok {
		t.Fatalf("message 0 content: %T", out[0].Content[0])
	}
	tu, ok := out[1].Content[0].(*brtypes.ContentBlockMemberToolUse)
	if !ok {
		t.Fatalf("message 1 content: %T", out[1].Content[0])
	}
	if *tu.Value.ToolUseId != "t1" || *tu.Value.Name != "bash" {
		t.Fatalf("tool_use block: %+v", tu.Value)
	}
	if tu.Value.Input == nil {
		t.Fatal("tool_use input document is nil")
	}
	tr, ok := out[2].Content[0].(*brtypes.ContentBlockMemberToolResult)
	if !ok {
		t.Fatalf("message 2 content: %T", out[2].Content[0])
	}
	if *tr.Value.ToolUseId != "t1" || tr.Value.Status != brtypes.ToolResultStatusError {
		t.Fatalf("tool_result block: %+v", tr.Value)
	}
	trText, ok := tr.Value.Content[0].(*brtypes.ToolResultContentBlockMemberText)
	if !ok || trText.Value != "a.go\nb.go" {
		t.Fatalf("tool_result content: %+v", tr.Value.Content)
	}
}

func TestBuildMessagesDropsEmptyBlocksAndMessages(t *testing.T) {
	empty := ""
	msgs := []ConverseMessage{
		{Role: "assistant", Content: []schema.ContentBlock{{Type: "text", Text: &empty}}},
		{Role: "user", Content: []schema.ContentBlock{{Type: "thinking"}}},
	}
	if out := buildMessages(msgs); len(out) != 0 {
		t.Fatalf("expected both messages dropped (empty text, unsupported type), got %+v", out)
	}
}

func TestBuildToolConfigOmitsChoiceForAutoAndNone(t *testing.T) {
	tools := []ConverseTool{{Name: "bash", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	for _, choiceType := range []string{"", "auto"} {
		cfg := buildToolConfig(tools, ConverseToolChoice{Type: choiceType})
		if cfg == nil {
			t.Fatalf("choice %q: expected non-nil config (tools present)", choiceType)
		}
		if cfg.ToolChoice != nil {
			t.Fatalf("choice %q: expected ToolChoice to stay unset, got %T", choiceType, cfg.ToolChoice)
		}
	}
}

func TestBuildToolConfigNoneOmitsToolConfigEntirely(t *testing.T) {
	// Bedrock's ToolChoice union has no "forbid tools" member (only
	// Auto/Any/Tool). "none" must not silently degrade to auto — the closest
	// faithful behavior is to send no ToolConfig at all, so the model has no
	// tools to call.
	tools := []ConverseTool{{Name: "bash", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	if cfg := buildToolConfig(tools, ConverseToolChoice{Type: "none"}); cfg != nil {
		t.Fatalf("choice %q: expected ToolConfiguration to be omitted entirely, got %+v", "none", cfg)
	}
}

func TestBuildToolConfigAnyAndTool(t *testing.T) {
	tools := []ConverseTool{{Name: "bash", InputSchema: json.RawMessage(`{}`)}}
	cfg := buildToolConfig(tools, ConverseToolChoice{Type: "any"})
	if _, ok := cfg.ToolChoice.(*brtypes.ToolChoiceMemberAny); !ok {
		t.Fatalf("any: got %T", cfg.ToolChoice)
	}
	cfg = buildToolConfig(tools, ConverseToolChoice{Type: "tool", Name: "bash"})
	tc, ok := cfg.ToolChoice.(*brtypes.ToolChoiceMemberTool)
	if !ok || *tc.Value.Name != "bash" {
		t.Fatalf("tool: got %+v", cfg.ToolChoice)
	}
}

func TestBuildToolConfigNoTools(t *testing.T) {
	if cfg := buildToolConfig(nil, ConverseToolChoice{}); cfg != nil {
		t.Fatalf("expected nil ToolConfiguration when there are no tools, got %+v", cfg)
	}
	// Same "no tools" path must hold even when tool_choice is "any": if every
	// tool got dropped upstream (parseTools), sending ToolChoiceMemberAny
	// against an empty tool list is a Bedrock ValidationException. The
	// len(tools)==0 guard must win regardless of choice type.
	if cfg := buildToolConfig(nil, ConverseToolChoice{Type: "any"}); cfg != nil {
		t.Fatalf("expected nil ToolConfiguration for tool_choice=any with no tools, got %+v", cfg)
	}
}

func TestToolResultTextFlattensStringAndBlockArray(t *testing.T) {
	if got := toolResultText(json.RawMessage(`"plain"`)); got != "plain" {
		t.Fatalf("string form: %q", got)
	}
	blockText := "from block"
	blocks := []schema.ContentBlock{{Type: "text", Text: &blockText}}
	raw, _ := json.Marshal(blocks)
	if got := toolResultText(raw); got != "from block" {
		t.Fatalf("block-array form: %q", got)
	}
	if got := toolResultText(nil); got != "" {
		t.Fatalf("nil: %q", got)
	}
}

// fakeCredSource is a providers.CredentialSource that needs no network.
type fakeCredSource struct {
	id, secret, session string
	expires             time.Time
	err                 error
	calls               int
}

func (f *fakeCredSource) Credentials(context.Context) (string, string, string, time.Time, error) {
	f.calls++
	if f.err != nil {
		return "", "", "", time.Time{}, f.err
	}
	return f.id, f.secret, f.session, f.expires, nil
}

// TestBrokerModeRejectsNilCredentialSource is fail-closed invariant #1
// (ADR-040): a nil source must be a construction ERROR — it must never
// silently fall through to the default credential chain and sign with the
// node's local, ungoverned IAM identity.
func TestBrokerModeRejectsNilCredentialSource(t *testing.T) {
	_, err := newAWSClient(context.Background(), "us-west-2", "broker", "", nil)
	if err == nil {
		t.Fatal("expected a construction error for broker mode with a nil credential source, got nil")
	}
	if !strings.Contains(err.Error(), "broker") {
		t.Errorf("error %q does not mention broker", err)
	}
	if !strings.Contains(err.Error(), "default AWS credential chain") {
		t.Errorf("error %q does not mention the default AWS credential chain", err)
	}
}

// TestBrokerModeFailsConstructionOnFetchError is fail-closed invariant #4
// (ADR-040): the first credential fetch is EAGER, so construction — not the
// first user request — is what fails. Verified fact §0.3: LoadDefaultConfig
// succeeds even when the injected provider always fails, so without the eager
// Retrieve a bad broker token would "succeed" at boot and only fail on user
// traffic. src.calls == 1 proves the eager fetch happened during construction.
func TestBrokerModeFailsConstructionOnFetchError(t *testing.T) {
	src := &fakeCredSource{err: errors.New("broker unreachable")}
	_, err := newAWSClient(context.Background(), "us-west-2", "broker", "", src)
	if err == nil {
		t.Fatal("expected construction to fail when the credential source errors, got nil")
	}
	if !strings.Contains(err.Error(), "broker unreachable") {
		t.Errorf("error %q does not contain the source's error", err)
	}
	if src.calls != 1 {
		t.Errorf("expected exactly 1 eager fetch during construction, got %d", src.calls)
	}
}

func TestBrokerModeCredentialsReachSigningConfig(t *testing.T) {
	src := &fakeCredSource{id: "ASIAFAKE", secret: "shhh", session: "tok", expires: time.Now().Add(time.Hour)}
	cache, err := brokerCredentials(context.Background(), src)
	if err != nil {
		t.Fatalf("brokerCredentials: %v", err)
	}
	creds, err := cache.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("cache.Retrieve: %v", err)
	}
	if creds.AccessKeyID != "ASIAFAKE" {
		t.Errorf("AccessKeyID = %q, want %q", creds.AccessKeyID, "ASIAFAKE")
	}
	if creds.SecretAccessKey != "shhh" {
		t.Errorf("SecretAccessKey = %q, want %q", creds.SecretAccessKey, "shhh")
	}
	if creds.SessionToken != "tok" {
		t.Errorf("SessionToken = %q, want %q", creds.SessionToken, "tok")
	}
	if !creds.CanExpire {
		t.Error("CanExpire = false, want true")
	}
	if creds.Expires.IsZero() {
		t.Error("Expires is zero, want the source's expiry")
	}
	// Verified fact §0.1: LoadDefaultConfig succeeds offline with an injected
	// credentials provider, so full client construction must work with no AWS
	// credentials and no network.
	ac, err := newAWSClient(context.Background(), "us-west-2", "broker", "", &fakeCredSource{id: "ASIAFAKE", secret: "shhh", session: "tok", expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("newAWSClient in broker mode with a working source: %v", err)
	}
	if ac == nil {
		t.Fatal("newAWSClient returned a nil client")
	}
}

func TestNonBrokerModesIgnoreCredentialSource(t *testing.T) {
	// Tolerance: non-broker modes go through LoadDefaultConfig, which can
	// legitimately fail in some environments — the assertion is only that
	// they must never take the broker branch (no broker nil-source error).
	for _, mode := range []string{"", "default", "irsa", "pod_identity", "static"} {
		_, err := newAWSClient(context.Background(), "us-west-2", mode, "", nil)
		if err != nil && strings.Contains(err.Error(), "broker") {
			t.Errorf("mode %q: got broker error %q; non-broker modes must never take the broker branch", mode, err)
		}
	}
}
