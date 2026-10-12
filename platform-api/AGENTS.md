# Platform API guidance

This file applies to the `platform-api/` subtree and supplements the repository
root `AGENTS.md`. Follow the root guide for shared workflow, commands, and final
validation, and `../TESTING.md` for repository-wide testing conventions.

**API design rules** (field types, validation, immutability, backward
compatibility, etc.) are in [`API-CONVENTIONS.md`](API-CONVENTIONS.md).
This file covers workflow, architecture, and review procedure only.

## Platform API and generated code

The private API types under `api/private/<version>/` are the source of truth.
The public API surface is derived from them and MUST NOT be edited by hand
(this includes `api/public/`, `zz_generated.*`, and generated `.schemas/*.yaml`
files).

After changing private types or markers, run:

```bash
make -C platform-api generate   # from repo root
# or
make generate                   # from inside platform-api/
```

Run generation twice and verify the second pass produces no changes.

## API design checklist

Before adding or changing an API resource, consider:

- Is the field desired configuration (`spec`) or observed state (`status`)?
- Does the change require a new independently addressable, persisted resource,
  or does it belong on an existing resource?
- Who owns each status field or condition?
- Is the API public, private, or partially public?
- If nested, what is its parent relationship and relationship field, and what
  happens when the referenced parent does not exist?
- Is the change additive and backward compatible?

## Adding a new API resource

| Step | Requirement |
| --- | --- |
| Define | Add the resource and list types under `api/private/<version>/` with the correct Kubebuilder markers. |
| Expose | Add `+orlop:public` only to types and fields intended for customers. |
| Register | Register both the resource and list type in the package `init()` function. |
| Relate | If the resource is nested, configure its parent relationship consistently in `getPrivateResources()` and `getPublicResources()`; inspect `NodePool` as the existing example. |
| Generate | Run `make -C platform-api generate` from the repository root, or `make generate` inside `platform-api/`. |
| Review | Confirm generated `GetResourceInfos()` includes the resource and inspect public types, conversions, deep-copy methods, and OpenAPI schemas. |
| Persist | Do not hand-write a database table solely for a new resource. Orlop creates the configured backing store from the registered `GroupKind`; add migrations only when the storage model itself changes. |
| Test | Cover registration and discovery, private/public filtering, direct and nested routes where applicable, plus customer happy and non-happy paths. |

## Architecture invariants

### Private and public API surfaces

The private API is served by Kubernetes `GenericAPIServer`; the public API uses
Orlop's router and filtered public types. Changes to registration, schemas, or
storage must preserve Kubernetes discovery, OpenAPI v2/v3, content negotiation,
and normal `kubectl` access on the applicable surface.

### Nested resources

- Use `NodePool` as the primary existing implementation to inspect before
  introducing a new nested-resource pattern.
- A child resource is independently stored and remains available through its
  normal Kubernetes resource path. A nested route is an additional parent-filtered
  view; it does not embed the child in the parent response.
- Configure nested resources in `cmd/platform-api-server/resources.go` by
  setting `ParentResource` in both `getPrivateResources()` and
  `getPublicResources()`, following the existing `NodePool` implementation.
- For a Cluster child, use the Cluster `GroupKind` and `Plural: "clusters"`.
  Follow the established relationship field for that resource; existing Gecko
  Cluster children such as `NodePool` use `spec.clusterID`.
- Keep the parent relationship values consistent across the private and public
  registrations. Test both direct and nested routes when changing this code.

## Testing and review

- Run `make -C platform-api lint` to run `golangci-lint` with
  `sigs.k8s.io/kube-api-linter` and further validate Platform API changes.
- API type or marker changes: regenerate, run `make -C platform-api test` from
  the repository root or `make test` inside `platform-api/`, and inspect the
  public types and OpenAPI schema diff.
- Router or nested-resource changes: cover discovery plus direct and nested
  collection/item behavior.
- Validation changes: test valid input, each invalid boundary, and omitted
  optional fields. Prefer table-driven Go tests when nearby tests use that style.
- Flag manual edits to generated public API, deep-copy, conversion, or schema
  files.
- Flag public fields missing `+orlop:public`, private implementation details
  exposed unintentionally, or API source changes without regenerated artifacts.
- Flag incompatible removal or renaming of a public API field or resource.
- Flag missing or inconsistent parent relationship configuration across the
  private and public registrations, or child resources embedded into Cluster
  responses solely for client convenience.

## Common pitfalls

1. Editing generated files instead of private API source types.
2. Forgetting `+orlop:public`, causing a field to disappear from the public API.
3. Adding a root type without registering both the resource and its list type.
4. Adding a child resource without configuring its parent relationship in both
   the private and public registrations.
5. Running a generator but ignoring legitimate changes to existing schemas,
   conversions, or deep-copy methods.
6. Testing only a nested endpoint and breaking standard Kubernetes access, or
   testing only the direct endpoint and missing parent filtering.
7. Duplicating condition ownership across related resources.
