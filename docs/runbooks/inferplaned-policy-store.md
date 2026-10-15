# Enable persisted budget settings on ECS

The console enables budget edits only when the policy API reports a writable
store and the signed-in session has write authority. A file-authoritative CP
returns `writable: false`; changing the browser's disabled attribute cannot
make it accept policy writes. ADR-038 supports a PostgreSQL policy store.

## Preserve the existing usage CP

For the memory-only demo, `deploy/ecs/policy-store.yaml` prepares a separate
policy CP and private PostgreSQL database. The existing CP and its retained
usage remain running. `scripts/prepare-policy-controlplane.py` adds exact
CloudFront routes for policies, synchronization, fleet status and SSO config.
All usage, inference, admin and static routes retain their existing origins.
Gateways must use the reviewed CloudFront CP URL for policy synchronization;
gateways configured with a direct legacy CP endpoint need an explicit endpoint
update before they can consume policies from the new service.

This is a deployment arrangement, not a new governance profile. Durable budget
authority remains opt-in and is not enabled by attaching the policy store.
The legacy CP lease issuer starts fresh on the policy CP; legacy allowances
are not durable global escrow. Existing local hard caps remain binding.
The single-AZ database and single policy replica are a demo configuration,
not a qualified HA deployment. Usage retains its existing bounded memory
history and loss-on-replacement behavior; it is not migrated into this DB.

## Secrets and database access

ECS injects the policy DSN from Secrets Manager. The CP execution role can read
only that DSN and its existing machine-token parameter, not the database master
secret. A one-shot bootstrap task receives the newly generated database
credentials and creates a separate `inferplane_cp` login. It rejects unsafe
passwords and unverified TLS and suppresses credential-bearing client errors.
Deployment tooling never retrieves secret values or operating records.

The encrypted database admits port 5432 only from the CP security group, requires
TLS and is reached with `sslmode=verify-full` and the official RDS CA bundle.
Seven-day backups, deletion protection and retained DB/secrets protect state.
The CP seeds the empty store from its unchanged image policy files once.
After seeding, DB policy documents are authoritative and files are not watched.
Existing OIDC issuer, client and allowed-group configuration must be preserved.
The heartbeat token remains read-only; it never gains policy-write authority.

## Prepare and deploy

Provisioning adds recurring database, Fargate, secret and network charges.
Review the current regional public AWS prices and obtain authorization for
those new resources before executing the change set.

1. Save existing deployment configuration privately. Validate the store
   template with cfn-lint and `policy-store-security.guard`.
2. Create and review the store change set and its `describe-events` validation.
   The first stage creates only isolated resources; `TaskDefinitionArn` is empty.
3. After approval, execute that stage. Run the bootstrap image as an ECS task;
   require a zero exit code. Do not retrieve its injected credentials.
4. Register the policy CP task from the existing pinned runtime/image and
   configuration. Add the RDS CA and DSN secret reference. Keep monetary
   authority/profile flags unchanged. Review the store update with its task
   ARN, then create the policy service and wait for healthy targets.
5. Prepare the main-stack routing candidate. Pass the provisioned VPC-origin ID
   as `PolicyVpcOriginId` and preserve every existing parameter. Review that
   only Distribution changes without replacement, then execute and wait for
   propagation. Invalidate the six policy/SSO/config paths.
6. Verify public routing/auth refusal, unchanged usage service, new service
   readiness, existing SSO configuration, and synthetic browser write controls.
   A human SSO completion or real policy mutation must be reported separately.

```bash
python3 scripts/prepare-policy-controlplane.py \
  --template "$PRIVATE_DIR/main.template.json" \
  --output "$PRIVATE_DIR/main.candidate.json"
python3 -m unittest discover -s tests/deployment -v
cfn-lint --regions ap-northeast-2 -t deploy/ecs/policy-store.yaml
```

Before the first write, the preserved old routes provide a rollback option.
After DB policy edits begin, rollback must keep the authoritative DSN and
documents. Reverting to image files could loosen or discard configured limits.

## Back up and restore policies

The policy store holds GovernancePolicy documents only. A policy export does
NOT capture budget authority balances, usage telemetry or identity
configuration, and an import never restores them. Restoring a balance would
rewrite central authority. For the database itself, keep relying on the
RDS/pg_dump backups described above.

Back up with the read token (for example, from cron):

```bash
curl -fsS -H "Authorization: Bearer $INFERPLANED_TOKEN" \
  "$CP/v1alpha1/config/export" -o "policies-$(date -u +%Y%m%dT%H%M%SZ).yaml"
```

To restore, preview first and then apply with the previewed generation. Use the
policy write token:

```bash
curl -fsS -X POST -H "Authorization: Bearer $INFERPLANED_POLICY_WRITE_TOKEN" \
  --data-binary @policies.yaml "$CP/v1alpha1/config/import?dry_run=true" | tee preview.json
GEN=$(jq -r .current_generation preview.json)
curl -fsS -X POST -H "Authorization: Bearer $INFERPLANED_POLICY_WRITE_TOKEN" \
  --data-binary @policies.yaml "$CP/v1alpha1/config/import?expected_generation=$GEN"
```

Import REPLACES the whole set. Every document missing from the file is
deleted. A `409` means policies changed after the preview; re-run the preview.
An empty file needs `allow_empty=true`. Files are capped at 64 MiB, and a set
larger than the 8 MiB JSON a data plane can receive is refused with `400`. A `500`
saying "committed but not applied" means the store holds the new set; re-run a
dry run to confirm what is distributed. The console offers the same flow under
Policies → Export YAML / Import (restore).

If the distribution was prepared before this change, its `/v1alpha1/config/*`
requests still reach the legacy CP origin. `prepare-policy-controlplane.py`
refuses an existing policy origin, so add the `/v1alpha1/config/*` behavior
(policy-cp origin, `forward-auth` origin request policy) by reviewed manual
edit. Then invalidate the six policy/SSO/config paths.
