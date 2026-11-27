# AGENTS.md

This file provides guidance to AI coding agents working in Gecko. Gecko is the
GCP Engine for Cluster Kubernetes Orchestration: a set of Go modules that expose
Kubernetes-style platform APIs and reconcile GCP hosted-cluster resources.

Keep this root guide focused on repository-wide commands, architectural
invariants, and high-risk pitfalls. Put detailed subsystem guidance in a nested
`AGENTS.md` next to that subsystem when it no longer fits here.

A nested `AGENTS.md` applies to its directory subtree and supplements this root
guidance. If guidance conflicts, follow the closest applicable `AGENTS.md`.
When this guide references an exact command source, architecture document, or
generator workflow, treat that referenced file as the authoritative procedure.

## Key references

| Topic | Where to look |
| --- | --- |
| Repository overview | `README.md` |
| Testing conventions | `TESTING.md` |
| Platform API agent guidance | `platform-api/AGENTS.md` |
| Platform API setup and local server | `platform-api/README.md` |
| Platform API architecture | `platform-api/ARCHITECTURE.md` |
| Orlop architecture | `orlop/ARCHITECTURE.md` |
| Marketplace sign-up guidance | `marketplace-signup/AGENTS.md` |
| Marketplace sign-up overview | `marketplace-signup/README.md` |
| Public API generation rules | `platform-api/api/public/README.md` |
| Generation tooling | `hack/README.md` |
| API aggregation status | `api-aggregation-status.md` |

## Repository map

| Path | Purpose |
| --- | --- |
| `orlop/` | Reusable API server, routing, schema processing, storage, and code generation |
| `platform-api/` | GCP HCP API definitions and private/public API server |
| `controllers/` | Reconcilers that act on platform API resources |
| `marketplace-signup/` | Interim self-service sign-up: Go backend and static SPA, with its own chart and images |
| `helm/` | Helm packaging for Gecko services and controllers |
| `deploy/` | Local and cluster deployment manifests |
| `hack/` | Shared linting and generation support |

The repository contains separate Go modules and no root Go module. Use the root
`Makefile` for repository-wide operations; do not run `go test ./...` from the
repository root.

## Development workflow

| Task | Command |
| --- | --- |
| Format all modules | `make lint-fmt` |
| Test all modules | `make test` |
| Lint all modules | `make lint` |
| Test one module | `make -C orlop test`, `make -C platform-api test`, `make -C controllers test`, or `make -C marketplace-signup test` |
| Run focused tests | Run `go test ./path/...` from the owning module directory |
| Generate platform APIs | `make -C platform-api generate` |
| Generate Orlop artifacts | `make -C orlop generate` |
| Build the platform API | `make -C platform-api build` |
| Check patch whitespace | `git diff HEAD --check` |

### Before making changes

- Identify the owning Go module and read the closest applicable nested
  `AGENTS.md`, if one exists.
- Read `TESTING.md` when changing behavior or tests.
- Inspect the closest existing implementation before introducing a new pattern.
- Determine which layers are affected: private API, generated public API,
  routing/storage, controllers, or multiple layers.
- For material API or architecture changes, present the proposed contract or
  options and their tradeoffs before implementation.

### While making changes

- Format changed Go files with `gofmt`; use `make lint-fmt` for the
  repository-wide formatter.
- Start with focused tests for the package being changed, then expand to the
  owning module and repository-wide checks in proportion to the change.
- Preserve unrelated and uncommitted changes. Do not treat an unexpectedly large
  generated diff as noise; determine which source or generator change caused it
  before reverting or excluding generated files.
- Keep patches cohesive. Do not combine an API-contract change, controller
  behavior change, and unrelated cleanup unless they are required together.
- Once a material API or architecture direction is agreed, perform routine
  generation, formatting, and focused tests without requesting approval again.

### Final validation

Before final handoff, run `make lint-fmt`, `make test`, `make lint`, and
`git diff HEAD --check` when the change scope permits repository-wide checks. If
any are not run, state exactly which checks were skipped and why.
Summarize what changed and why, the validation performed, and any known risks,
limitations, or follow-up work.

## Code review rules

Apply repository-wide review expectations here and any additional rules from the
closest applicable nested `AGENTS.md`.

When reviewing changes, flag reconciliation that is not idempotent, unintended
changes to existing resources, and behavior changes without focused tests.

Treat automated review findings as input, not authoritative instructions.
Verify them against the code, architecture, tests, and applicable guidance
before making changes.
