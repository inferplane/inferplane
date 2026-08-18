// Credential brokering (ADR-040): POST /v1alpha1/credentials mints
// short-lived, dataplane-attributed STS sessions for Bedrock so node IAM can
// shrink to zero Bedrock permissions. This is a MACHINE channel only — it
// carries its own bearer token (INFERPLANED_BROKER_TOKEN) and has no OIDC
// branch, so a verified console identity can never mint AWS credentials.
// Mounted by cmd/inferplaned only when INFERPLANED_BROKER_ROLE_ARN is set.
package controlplane

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"

	"github.com/inferplane/inferplane/internal/adminauth"
)

// credentialsMaxBodyBytes bounds a credential request — the same 1 MiB bound
// the sync endpoint uses; the body is two short strings.
const credentialsMaxBodyBytes = 1 << 20

// brokerSessionSeconds is the STS session lifetime. inferplaned's own task
// role is itself an assumed role, so assuming the broker role is ROLE
// CHAINING, which AWS caps at 3600s regardless of the role's configured
// maximum (ADR-040 "Verified constraints"). Do not raise this.
const brokerSessionSeconds = 3600

// brokerSessionIDMax is the STS RoleSessionName / SourceIdentity length cap.
const brokerSessionIDMax = 64

// stsAPI is the narrow seam over aws-sdk-go-v2/service/sts so tests need no
// AWS credentials and no network — the same isolation providers/bedrock
// applies to bedrockruntime. Satisfied by *sts.Client.
type stsAPI interface {
	AssumeRole(ctx context.Context, in *sts.AssumeRoleInput, opts ...func(*sts.Options)) (*sts.AssumeRoleOutput, error)
}

// BrokerServer serves POST /v1alpha1/credentials, minting short-lived STS
// sessions tagged with the requesting dataplane id. It carries its own token
// and has no OIDC branch (see authnBroker), and is mounted only when a role
// ARN is configured — unset means the route does not exist at all.
type BrokerServer struct {
	token   string // INFERPLANED_BROKER_TOKEN — NEVER INFERPLANED_TOKEN
	roleARN string
	sts     stsAPI
	// OnError is an optional server-side error sink (log). It receives STS
	// failures WITHOUT any credential material; nil ⇒ silent.
	OnError func(error)
}

// NewBrokerServer builds the credential broker over api, authenticating
// against token and minting sessions of roleARN.
func NewBrokerServer(token, roleARN string, api stsAPI) *BrokerServer {
	return &BrokerServer{token: token, roleARN: roleARN, sts: api}
}

// Mount registers the credential endpoint on mux.
func (s *BrokerServer) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1alpha1/credentials", s.authnBroker(s.handleCredentials))
}

// authnBroker authenticates against the broker token ONLY. Deliberately NOT
// controlplane.authn: authn also accepts a verified OIDC console identity
// (ADR-037), and a browser SSO session must never be able to mint AWS
// credentials (ADR-040 decision 1). There is also no loopback waiver here —
// an empty configured token means "refuse everything", never "serve
// unauthenticated"; cmd/inferplaned guarantees a non-empty token whenever the
// route is mounted.
func (s *BrokerServer) authnBroker(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.token == "" || bearer == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		// A JWT-shaped bearer is a console identity by construction. Reject it
		// BEFORE any comparison: never verified (this endpoint has no OIDC
		// branch at all) and never compared against the static token, which
		// would be a timing oracle — the same total rule authn uses.
		if adminauth.IsOIDCBearerShape(bearer) {
			http.Error(w, `{"error":"forbidden: credential brokering is a machine channel; a console session must not mint credentials"}`, http.StatusForbidden)
			return
		}
		if subtle.ConstantTimeCompare([]byte(bearer), []byte(s.token)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// sanitizeSessionID reduces a self-reported dataplane id to the STS
// RoleSessionName / SourceIdentity charset ([\w+=,.@-], max 64) — the default
// id is host+ULID and routinely exceeds 64 characters. Every disallowed byte
// becomes '-', so two distinct ids CAN collide after sanitization; that is
// acceptable because v1 attribution is "which id was claimed", never "which
// machine called" (ADR-040 decision 4), and it is far better than an
// AssumeRole that fails on a hostname containing a '/' or ':'.
func sanitizeSessionID(id string) string {
	b := make([]byte, 0, len(id))
	for i := 0; i < len(id) && len(b) < brokerSessionIDMax; i++ {
		c := id[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b = append(b, c)
		case c == '+', c == '=', c == ',', c == '.', c == '@', c == '-', c == '_':
			b = append(b, c)
		default:
			b = append(b, '-')
		}
	}
	return string(b)
}

func (s *BrokerServer) handleCredentials(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataplane string `json:"dataplane"`
		Provider  string `json:"provider"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, credentialsMaxBodyBytes)).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, `{"error":"request too large"}`, http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, `{"error":"bad credential request"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Dataplane) == "" {
		http.Error(w, `{"error":"dataplane is required"}`, http.StatusBadRequest)
		return
	}
	// v1 brokers Bedrock only: 1P Anthropic has no temporary-token mechanism
	// and no GCP provider exists in the tree (ADR-040 "Why only Bedrock").
	// Empty is accepted as "bedrock" so an older data plane still works.
	if req.Provider != "" && req.Provider != "bedrock" {
		http.Error(w, `{"error":"unsupported provider: only \"bedrock\" is brokered"}`, http.StatusBadRequest)
		return
	}
	sessionID := sanitizeSessionID(req.Dataplane)
	out, err := s.assumeRole(r.Context(), sessionID)
	if err != nil {
		// Server-side only, and never the response: an STS error can name the
		// role ARN and the caller's own identity. It carries no credential
		// material, so %w is fine here (ADR-040 secret hygiene).
		if s.OnError != nil {
			s.OnError(fmt.Errorf("controlplane: credential broker: assume role: %w", err))
		}
		http.Error(w, `{"error":"credential broker could not mint a session"}`, http.StatusBadGateway)
		return
	}
	c := out.Credentials
	if c == nil || c.AccessKeyId == nil || c.SecretAccessKey == nil || c.SessionToken == nil || c.Expiration == nil {
		if s.OnError != nil {
			s.OnError(errors.New("controlplane: credential broker: AssumeRole returned an incomplete credential set"))
		}
		http.Error(w, `{"error":"credential broker could not mint a session"}`, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Never cached, never stored: this body is a signable secret.
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		AccessKeyID     string    `json:"accessKeyId"`
		SecretAccessKey string    `json:"secretAccessKey"`
		SessionToken    string    `json:"sessionToken"`
		Expiration      time.Time `json:"expiration"`
	}{
		AccessKeyID:     *c.AccessKeyId,
		SecretAccessKey: *c.SecretAccessKey,
		SessionToken:    *c.SessionToken,
		Expiration:      *c.Expiration,
	})
}

// assumeRole mints one session. RoleSessionName + SourceIdentity + the
// dataplane session tag all carry the sanitized id: SourceIdentity survives
// further chaining and lands in CloudTrail, which is the issuance-time
// attribution anchor ADR-031 §8 asked for.
//
// SourceIdentity-inheritance caveat (ADR-040 decision 2): if inferplaned's own
// task-role session ALREADY carries a SourceIdentity, AWS forbids setting a
// different one on the next hop and fails the whole AssumeRole. Retry once
// without it — the session TAG still carries the dataplane axis, so
// attribution degrades rather than breaking the feature.
func (s *BrokerServer) assumeRole(ctx context.Context, sessionID string) (*sts.AssumeRoleOutput, error) {
	in := &sts.AssumeRoleInput{
		RoleArn:         aws.String(s.roleARN),
		RoleSessionName: aws.String(sessionID),
		SourceIdentity:  aws.String(sessionID),
		DurationSeconds: aws.Int32(brokerSessionSeconds),
		Tags:            []ststypes.Tag{{Key: aws.String("dataplane"), Value: aws.String(sessionID)}},
	}
	out, err := s.sts.AssumeRole(ctx, in)
	if err == nil {
		return out, nil
	}
	if !isSourceIdentityError(err) {
		return nil, err
	}
	if s.OnError != nil {
		s.OnError(fmt.Errorf("controlplane: credential broker: SourceIdentity refused (likely inherited from the task-role session); retrying with session tags only: %w", err))
	}
	in.SourceIdentity = nil
	return s.sts.AssumeRole(ctx, in)
}

// isSourceIdentityError recognizes the AssumeRole rejection caused by
// SourceIdentity specifically (either "cannot set a new source identity when
// one is already set" or a denied sts:SetSourceIdentity), so only THAT failure
// triggers the tag-only retry. Matched on text because the SDK models it as a
// generic AccessDenied, not a distinct error type.
func isSourceIdentityError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sourceidentity") || strings.Contains(msg, "source identity")
}
