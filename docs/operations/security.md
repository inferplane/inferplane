# Security boundaries

inferplane protects credential separation and applies configured controls at the gateway. Its enforcement depends on trusted deployment boundaries, correct identity/provider declarations, and the selected storage profile.

## Credentials and administration

Provider secrets are server-side `env:`/`file:` references; never put secret values into a committed config. Virtual keys are hashed at rest and shown once at issuance. Do not forward the virtual key as an upstream provider credential.

Keep management access private. Policy writes and credential brokering have dedicated credentials; the heartbeat token is not an administrator or broker token. Existing admin checks do not implement the complete six-role org/team capability model.

OIDC identity must come from verified issuer/subject claims. A key's free-form owner or an email is not equivalent to required [verified identity](../verified-identity.md). Store/DSN access is privileged administration.

## Host and network trust

A person controlling a gateway host can obtain locally accessible provider credentials, broker tokens or short-lived sessions. Brokering removes standing Bedrock IAM credentials but does not make a compromised developer laptop bypass-proof. Constrain direct upstream access in the infrastructure when mediation is a requirement.

Keep admin/metrics endpoints restricted and use TLS for remote traffic. Shared admission requires reachable, authenticated Postgres; it fails closed on loss. Configure provider destinations and network egress together—an operator-assigned “internal” label does not establish an actual private boundary.

## Privacy and audit

Sensitive-data detectors are finite heuristics. Validate the relevant data classes and refusal paths; do not infer universal PII detection or regulatory compliance. Masking and protocol conversion can change request bytes and cache behavior.

Request/response body capture is opt-in and requires its own encryption, access and retention controls. Tamper-evident audit chains detect inconsistency; external WORM resistance additionally depends on [anchor storage and IAM](../runbooks/audit-anchoring.md). Preserve every instance segment.

## Report a vulnerability

Use the [repository security policy](../../SECURITY.md) for private reporting. Do not publish credentials, raw prompts, identity declarations, or exploitable details in an ordinary issue. Public issue templates are appropriate for sanitized non-security defects.
