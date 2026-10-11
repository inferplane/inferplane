# Deploy the control-plane dashboard through CloudFront

## Separate static assets from financial state

The optional CloudFront deployment publishes **only the data-free shell** from
`internal/controlplane/ui/static/`. It does not rebuild or restart inferplaned,
mayu, databases, policy stores or financial authorities. This avoids discarding
a memory-only CP's recent usage history merely to replace its UI. The embedded
console remains available on the CP itself; the CDN can serve a newer static
release while its APIs keep running on the existing CP image.

A private, encrypted, versioned S3 REST bucket is readable only by the existing
distribution through SigV4 OAC, with public access blocked. Static paths are
exact: `/ui/`, `/ui/index.html`, `/ui/app.js`, `/ui/style.css`. **Do not route
`/ui/*` to S3**: `/ui/auth/config` must stay on the authenticated-service origin's
public, secret-free SSO configuration endpoint. All `/v1alpha1/*`, inference and
admin behaviors retain their existing origins, Authorization forwarding and
query handling. A CloudFront Function redirects only `/` and `/ui` to `/ui/`;
all other requests pass through unchanged.

Static cache is disabled and responses use `Cache-Control: no-store`. Security
headers retain the CP's exact CSP, including configured Cognito login origins,
plus nosniff, no-referrer and frame denial. Only the shell is published; never
upload config files, environment files, credentials, policy documents or usage.
No new compute instances or distributions are needed. S3 storage/requests and
CloudFront Function requests still incur their normal small AWS charges.

## Prepare and review

Support resources are declared in `deploy/cloudfront/dashboard-resources.yaml`,
with security rules in `deploy/cloudfront/dashboard-security.guard`.
`prepare-cloudfront-dashboard.py` prepares a candidate and rollback template;
it does **not** deploy them. It accepts the original `get-template` response or
a JSON template and requires the existing logical ID `Distribution`. Reading a
YAML TemplateBody requires PyYAML as an authoring dependency.

1. Save the current template, parameter names, distribution config and ECS
   deployment/task state to a private directory (umask 077). Capture the exact
   CSP from the current `/ui/` response. Do not log tokens or DSNs.
2. Validate the support template with cfn-lint and cfn-guard; create and inspect
   its change set and pre-deployment validation events before executing.
3. Hash the three shell files. Upload the four exact object keys below an
   immutable `releases/<content-hash>/` prefix, with correct content types and
   no-store. The `ui/` object itself contains index.html: it is not public
   website hosting, and S3 does not supply an automatic index document.
4. Prepare the main-stack update from the **original** template and **live**
   distribution. Preserve the existing domain, ACM certificate and private VPC
   origins. Never normalize untouched scalar GetAtt expressions: that alone
   can force an ECS task-definition replacement. Use previous values for every
   old stack parameter. The helper refuses unknown origin IDs or existing edge
   functions rather than silently replacing them.
5. Lint the candidate and rollback. Inspect the main change set: **only
   Distribution may be modified, without replacement**. If any ECS service,
   TaskDef, IAM, network or state resource appears, stop and fix the candidate.
6. Check the distribution ETag for concurrent changes, execute the reviewed
   change set, wait for stack UPDATE_COMPLETE and distribution Deployed, then
   invalidate `/`, `/ui` and `/ui/*`.

```bash
python3 scripts/prepare-cloudfront-dashboard.py \
  --template "$PRIVATE_DIR/stack-template.before.json" \
  --distribution-config "$PRIVATE_DIR/cloudfront.before.json" \
  --support-outputs "$PRIVATE_DIR/support.outputs.json" \
  --release-prefix "$RELEASE_PREFIX" \
  --output "$PRIVATE_DIR/template.dashboard.json"
python3 -m unittest discover -s tests/deployment -v
cfn-lint --regions ap-northeast-2 -t deploy/cloudfront/dashboard-resources.yaml
```

## Verify and roll back

Check deployed file hashes against the release manifest; root redirect; exact
CSP and no-store; direct S3 anonymous access denied; protected APIs returning 401
without a bearer; configured reads returning 200; real browser navigation,
mobile layout, disconnect and the Cognito authorization redirect. An IdP
redirect check is not proof of a completed human SSO login or write admission.
Do not test writes by altering a real financial policy. Confirm ECS task IDs and
service revision are unchanged.

The helper writes `.rollback.json` and `.rollback.parameters.json` alongside
its candidate. They restore the **adopted live baseline** (including the domain,
TLS and VPC origins), not the historical template that omitted these manual
updates. Review and execute rollback as a CloudFormation change set with only
Distribution modified, then invalidate the same UI paths. Support assets remain
retained/private; do not delete the bucket or functions while attached.

For subsequent releases, upload a new hashed prefix and update only the existing
`DashboardReleasePrefix` parameter using the current template; retain all other
parameters and re-review the change set. The preparation helper deliberately
handles initial attachment only, not automatic replacement of existing edge
associations. If SSO origins change, review the support stack's CSP parameter too.

The operator UI workflow and its capability limits are described in the
[dashboard runbook](inferplaned-dashboard.md).
