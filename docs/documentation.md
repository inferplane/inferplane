# Maintain the documentation

The product site is built from repository Markdown with MkDocs Material and published by GitHub Actions to **https://inferplane.github.io/inferplane/**. The site describes the current `main` branch; it is not a versioned API compatibility promise.

## Preview and validate

```bash
python3 -m venv .venv-docs
.venv-docs/bin/python -m pip install -r requirements-docs.txt
.venv-docs/bin/python -m mkdocs serve
```

Open the loopback URL printed by MkDocs. To run the same documentation checks as CI:

```bash
.venv-docs/bin/python -m unittest discover -s tests/docs -v
.venv-docs/bin/python -m mkdocs build --strict
.venv-docs/bin/python scripts/check_docs_site.py site
```

Dependencies are pinned in `requirements-docs.txt`. To update intentionally, change `requirements-docs.in`, resolve a new lock with `uv pip compile --generate-hashes`, and review the dependency diff.

## Editorial contract

Write for the operator's task: prerequisites, supported configuration, expected result, failure behavior, and next step. Keep alpha status and profile-specific limits visible. Distinguish implementation, automated testing, live-model compatibility and production qualification.

New behavior must link to its reference and decision. New operational procedures must preserve credentials, identity history, audit evidence and outstanding authority. Never turn examples into claims about current model availability or pricing.

`mkdocs.yml` owns navigation. Historical specifications/plans and customer analysis remain in GitHub; they are excluded from site navigation and search. ADRs remain searchable with their original status. The hook rewrites links to repository-only files/directories into GitHub source links and generates an ADR index without duplicating source documents.

## Publication

The documentation workflow validates every PR. Only a successful build from a push to `main`, or a manual run on `main`, can deploy. The deployment job alone has Pages-write/OIDC permissions and uses the `github-pages` environment. Concurrent publications are serialized.

Repository administrators must configure Pages to use **GitHub Actions**. The first deployment creates the published site. Review a preview/build before merging; publication follows the repository's required AI review and CI gates.

For future changes, check the deployed URL, navigation, search, mobile layout and deep links after the workflow succeeds. Never bypass review checks to publish documentation.
