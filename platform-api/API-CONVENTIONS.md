# Platform API Conventions

Supplements the upstream [Kubernetes API conventions](https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md); takes precedence where the two conflict. Key words follow [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119).

---

## Private vs public API surface

`api/private/<version>/` is the **source of truth**. `api/public/<version>/` is generated and MUST NOT be edited by hand.

- Add `// +orlop:public` to any field or type that MUST appear in the public API. Everything else is stripped from public responses.
- After changing private types, run `make -C platform-api generate` and verify the second pass produces no changes.

```go
type ClusterStatus struct {
    // +orlop:public
    Conditions []metav1.Condition `json:"conditions,omitempty"` // ← visible

    PlacementResult *PlacementResult `json:"placementResult,omitempty"` // ← stripped
}
```

---

## Resource structure

```go
// <Resource> one-line description.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
type MyResource struct {
    metav1.TypeMeta   `json:",inline"`
    // +optional
    metav1.ObjectMeta `json:"metadata,omitempty"`
    // +orlop:public
    // +required
    Spec   MyResourceSpec   `json:"spec,omitempty"`
    // +orlop:public
    // +optional
    Status MyResourceStatus `json:"status,omitempty"`
}
```

- `Spec` — desired configuration; `+required` when the resource has meaningful config.
- `Status` — observed state written only by controllers; `+optional`.
- `+kubebuilder:subresource:status` MUST be present whenever `Status` exists.
- Both the resource type and its list type MUST be registered in `init()`.

---

## Field design

**Optionality** — every field MUST carry exactly one of `+optional` or `+required`.
- `+required` MUST NOT have `omitempty`.
- `+optional` MUST have `omitempty` and use a pointer or nilable type (`*T`, `[]T`, `map[K]V`).

**Naming**
- Go: PascalCase. JSON: camelCase.
- Durations: `fooSeconds` (`int32`/`int64`). Never `time.Duration`.
- Timestamps: `somethingTime` (`metav1.Time`). Not `At`, `Stamp`, or `Timestamp`.
- Booleans: prefer a string alias when a third state is plausible. Never `IsFoo`; use `Foo`.
- No abbreviations except well-established ones (`id`, `url`).

**Types**
- Integers: `int32` or `int64`. Never `int` (width is platform-specific) or unsigned types. Unsigned integers are avoided across Kubernetes APIs because JSON numbers are untyped and many client libraries (JavaScript, Python) cannot safely represent values above 2⁵³; negative values are instead rejected with `+kubebuilder:validation:Minimum=0`.
- Floats: MUST NOT appear in `Spec`. Use an integer with the unit in the name (`DiskSizeGB int32`).
- Enums: string type aliases, CamelCase values, declared with `+kubebuilder:validation:Enum=A;B;C`.
- Defaults: `// +default=<value>` for scalars. List defaults cannot be expressed as markers; document in the comment.

**Prefer standard markers over kubebuilder-specific equivalents.** Where a
standard controller-tools or upstream marker exists, use it instead of the
`+kubebuilder:` prefixed variant. Standard markers are toolchain-agnostic and
work with a wider range of generators.

| Prefer | Over |
|---|---|
| `// +default=<value>` | `// +kubebuilder:default=<value>` |
| `// +optional` | `// +kubebuilder:validation:Optional` |
| `// +required` | `// +kubebuilder:validation:Required` |
| `// +listType=<type>` | `// +kubebuilder:validation:ListType=<type>` |

**Documentation** — every field MUST have a comment starting with the JSON field name:

```go
// diskSizeGB is the size of the boot disk in gigabytes. Defaults to 64.
// +optional
// +default=64
// +kubebuilder:validation:Minimum=20
// +kubebuilder:validation:Maximum=65536
DiskSizeGB int32 `json:"diskSizeGB,omitempty"`
```

**Public field doc-strings are customer-facing documentation.** Comments on fields
marked `+orlop:public` are extracted verbatim into the generated OpenAPI schema and
surface in public API reference documentation. Therefore:

- MUST describe the field from a customer's perspective.
- MUST NOT contain internal implementation details, management-cluster names, RH-internal
  abbreviations, or any information from the [Internal information](#internal-information)
  list below.
- If additional context is needed for operators or developers, put it in the private-only
  field's comment or in an internal design document — not in the public field doc-string.

---

## Validation

**Prefer in-code validation** (`ValidateCreate` / `ValidateUpdate`) because it can be fully covered by unit tests without a running API server.

```go
func (c *Cluster) ValidateCreate(ctx context.Context) error {
    if c.Spec.SafeName != DefaultSafeName(c.Name, c.UID) {
        return fmt.Errorf("spec.safeName must be the default safe name for metadata.name")
    }
    return nil
}
```

**Use CEL** (`+kubebuilder:validation:XValidation`) for:
1. Simple self-contained rules that are obvious from the marker — immutability guards, format patterns.
2. Validation copied verbatim from HyperShift (keeps divergence low).

**Scalar bounds** MUST always be declared as markers regardless of where other validation lives — they are enforced before any `ValidateCreate` hook and appear in the OpenAPI schema:
- Strings: `+kubebuilder:validation:MaxLength=<N>`
- Numbers: `+kubebuilder:validation:Minimum` / `Maximum`
- Lists/maps: `+kubebuilder:validation:MaxItems` / `MaxProperties`

CEL error messages MUST describe what the value must look like, not just that it is invalid.

**Narrowness** — validation SHOULD be as narrow as possible. Relaxing it later is safe; tightening is a breaking change. Fields mapped to HyperShift MUST use the same or more restrictive validation.

### Validator performance and safety

Every admission hook runs synchronously on the critical path and affects all tenants.

- **Only the platform-api database may be accessed.** Validators MUST NOT call any service outside the API server process — including Cincinnati, management clusters, or GCP APIs. The only permitted I/O is reading objects from the platform-api's own storage backend (PostgreSQL or Spanner). Information not already in that database MUST be mirrored there by a controller first.
- **Local computation is fine.** String parsing, date arithmetic, recurrence rule evaluation, and similar in-process calculations are not a concern. The `ControlPlaneUpgradePolicy` maintenance window validator is a good example.

---

## Immutability

```go
// Fully immutable after creation:
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterID is immutable"

// May be set once (e.g. auto-generated by a controller):
// +kubebuilder:validation:XValidation:rule=`oldSelf == "" || self == oldSelf`,message="infraID is immutable"
```

Do not use `+immutable` — it has no defined semantics in Kubebuilder.

---

## List fields and server-side apply

All slice fields MUST declare `+listType`:

| `+listType` | Use when | Extra markers |
|---|---|---|
| `atomic` | List is replaced as a unit | — |
| `set` | Scalars, each element unique | — |
| `map` | Structs with a natural key | `+listMapKey=<field>` per key |

`Conditions` MUST always use:
```go
// +listType=map
// +listMapKey=type
// +patchStrategy=merge
// +patchMergeKey=type
Conditions []metav1.Condition `json:"conditions,omitempty"`
```

---

## Status and conditions

- Use `[]metav1.Condition`. The `phase` field pattern is deprecated.
- Conditions MUST use `metav1.Condition` directly. Required fields: `Type` (PascalCase, positive polarity preferred), `Status` (`True`/`False`/`Unknown`), `Reason` (one CamelCase word), `Message` (actionable sentence), `LastTransitionTime` (update only when `Status` changes), `ObservedGeneration`.
- Every status field MUST have exactly one owning controller. Do not duplicate lifecycle reporting across parent and child.
- Controllers MUST be able to reconstruct all status from spec and observed state.
- Fields set by operators belong in `spec`. Fields computed by controllers belong in `status`. Never mix them.

---

## Internal information

The following MUST NOT carry `+orlop:public`:

- Management cluster names or anything identifying internal RH infrastructure.
- Internal network addresses unreachable by end-users.
- References to internal RH systems or abbreviations.
- Credentials or secrets.
- Operator-tuning knobs (e.g. `autoApproveThreshold`).

Document why a field is private in its comment:
```go
// autoApproveThreshold is operator-set. Private API only.
// +optional
AutoApproveThreshold *int32 `json:"autoApproveThreshold,omitempty"`
```

---

## Quota and admission enforcement

Per-namespace creation limits are enforced synchronously in `ValidateCreate`:
- `Cluster`: 50 per namespace (`hostedclusters` quota).
- `NodePool`: 500 per namespace (`nodepools` quota).
- Exceeding a limit returns `HTTP 403` with the current count and limit.

Limits are managed through `Quota` and `QuotaRequest` resources.

---

## Verb restrictions

Use `+orlop:public-verbs` on the type doc comment to restrict public verbs. Unlisted verbs return `501 Not Implemented`.

```go
// +orlop:public-verbs: list,get
// +kubebuilder:object:root=true
type Quota struct { ... }
```

Valid verbs: `create`, `get`, `list`, `update`, `patch`, `delete`, `watch`.

| Pattern | Verbs | Why |
|---|---|---|
| Read-only platform data (`Quota`, `Version`, `Channel`) | `list,get` | Customers view only |
| User requests (`QuotaRequest`) | `create,get,list,delete` | Immutable spec; approved requests are audit records |
| Full-lifecycle resources (`Cluster`, `NodePool`) | _(all — no annotation)_ | Standard CRUD |

---

## Backward compatibility

The public API is a contract. Published fields and resources MUST NOT be removed, renamed, or have validation tightened. Safe changes: adding optional fields, widening validation, adding enum values. Breaking changes require a new API version and a migration plan.

Private-only fields are not subject to the public guarantee but MUST remain compatible with existing controller deployments.
