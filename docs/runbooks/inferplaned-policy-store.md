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
   propagation. Invalidate the five policy/SSO paths.
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
