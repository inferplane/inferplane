# Make your first request

Run one local gateway, issue a virtual key, and send an Anthropic Messages request. This evaluation uses SQLite, loopback listeners, and local audit files. It does not deploy shared enforcement.

## Before you start

You need Git, the Go toolchain required by `go.mod`, curl, OpenSSL, and an Anthropic API key with access to the example model. Requests incur your provider's normal charges. For a different provider, adapt the [self-hosted example](../../examples/config.selfhosted.json) or use the [client and provider guide](clients.md).

## 1. Build and prepare private state

Run these commands from the repository root in a Bash terminal:

```bash
git clone https://github.com/inferplane/inferplane.git
cd inferplane
CGO_ENABLED=0 go build -trimpath -o bin/mayu ./cmd/mayu
umask 077
mkdir -p .local/inferplane
```

The [quickstart configuration](../../examples/config.quickstart.json) stores keys and audit files under `.local/inferplane/`, relative to this directory. Both listeners bind to `127.0.0.1`.

## 2. Supply server credentials

```bash
read -rsp 'Anthropic API key: ' ANTHROPIC_API_KEY
echo
export ANTHROPIC_API_KEY
INFERPLANE_ADMIN_TOKEN="$(openssl rand -hex 32)"
export INFERPLANE_ADMIN_TOKEN

bin/mayu pricing check --config examples/config.quickstart.json
```

The config contains only secret references. The provider key stays in the server terminal. `pricing check` must pass before continuing; the bundled catalog is an estimate for internal accounting, not a current provider quote. Confirm model access and applicable rates for your account before evaluating financial limits.

## 3. Issue a client key and start the gateway

```bash
bin/mayu keys create \
  --config examples/config.quickstart.json \
  --team demo --models claude-sonnet-4-6

bin/mayu serve --config examples/config.quickstart.json
```

Store the first output line, the virtual key, in your secret manager. Plaintext is shown once. The following `key_id` is metadata, not a usable credential. Using `--config` ensures issuance and the gateway use the same database.

## 4. Verify from another terminal

```bash
curl --fail-with-body http://127.0.0.1:9090/readyz

read -rsp 'Virtual key: ' INFERPLANE_API_KEY
echo
export INFERPLANE_API_KEY

curl --fail-with-body http://127.0.0.1:8080/v1/messages \
  -H "x-api-key: $INFERPLANE_API_KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'content-type: application/json' \
  -d '{
    "model": "claude-sonnet-4-6",
    "max_tokens": 64,
    "messages": [{"role": "user", "content": "Reply with: gateway connected"}]
  }'
```

Expect readiness HTTP 200 and a Messages response containing text and usage. A 401 usually means the wrong virtual key or database; an upstream access error means the server's provider credentials/model access need attention.

To try Claude Code from the client terminal:

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:8080 \
ANTHROPIC_API_KEY="$INFERPLANE_API_KEY" \
claude --model claude-sonnet-4-6
```

## 5. Inspect and stop

The admin console is at `http://127.0.0.1:9090/admin/ui/`; authenticated actions need the server's admin token. In another terminal at the repository root:

```bash
bin/mayu report --file .local/inferplane/audit.jsonl --by team,model
bin/mayu audit verify --file .local/inferplane/audit.jsonl
```

Stop the server with Ctrl-C. Keep state files to preserve key records and audit evidence. Local monetary/rate/token counters are in memory; restarting is not a durable budget continuation.

Next, [choose a deployment profile](deployment-profiles.md), review [security boundaries](../operations/security.md), and configure [routing and policy](../policy-routing.md). Do not expose the quickstart listeners by changing their bind addresses before selecting authentication, TLS, and network controls.
