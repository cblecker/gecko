# Orlop API Server Architecture

## Dual API Surface with Shared Storage

```
┌─────────────────────────────────────────────────────────────────────┐
│                          API Clients                                │
└──────────────┬─────────────────────────────┬────────────────────────┘
               │                             │
               │                             │
       ┌───────▼────────┐            ┌───────▼────────┐
       │  Private API   │            │   Public API   │
       │   Port 8080    │            │   Port 8081    │
       └───────┬────────┘            └───────┬────────┘
               │                             │
               │                             │
       ┌───────▼────────┐            ┌───────▼────────┐
       │ Private Schema │            │ Public Schema  │
       │  (Internal)    │            │  (Filtered)    │
       └───────┬────────┘            └───────┬────────┘
               │                             │
               │                    ┌────────▼────────┐
               │                    │   Converter     │
               │                    │ PrivateToPublic │
               │                    │ PublicToPrivate │
               │                    └────────┬────────┘
               │                             │
               └──────────┬──────────────────┘
                          │
                  ┌───────▼───────┐
                  │ Shared Store  │
                  │  (In-Memory)  │
                  │               │
                  │  Objects are  │
                  │ stored in the │
                  │ PRIVATE type  │
                  └───────────────┘
```

## API Flow Examples

### Creating an Object via Private API

```
Client → POST /apis/.../v1/namespaces/default/objects
         {
           spec: {publicField: "val", internalField: "secret"},
           metadata: {
             labels: {"app": "web", "private.orlop.../key": "value"}
           }
         }
         │
         ▼
  Private Schema Validation
         │
         ▼
  Store (Private Type) ✓ All fields stored
```

### Reading via Public API

```
Client → GET /apis/.../v1/namespaces/default/objects/test
         │
         ▼
  Store (Private Type) → {spec: {publicField, internalField}, 
                          metadata: {labels: {app, private.orlop...}}}
         │
         ▼
  Converter.PrivateToPublic()
    1. JSON round-trip (drops internalField)
    2. filterPrivateMetadata() (removes private.orlop... labels/annotations)
    3. filterPrivateConditions() (removes private.orlop... conditions)
         │
         ▼
  Client ← {spec: {publicField: "val"},
            metadata: {labels: {"app": "web"}}}
            ✓ Internal fields hidden
```

### Updating via Public API

```
Client → PUT /apis/.../v1/namespaces/default/objects/test
         {
           spec: {publicField: "newval"},
           metadata: {resourceVersion: "5"}
         }
         │
         ▼
  Converter.PublicToPrivate(public, existing)
    1. Start with existing private object (preserves internalField)
    2. Overlay public fields from request
    3. Result: {spec: {publicField: "newval", internalField: "secret"}}
         │
         ▼
  Store (Private Type) ✓ Internal fields preserved
```

## Key Components

### Shared Storage
- Single in-memory store per resource type
- Objects stored in **private type** format
- Both APIs access the same store instances
- Created in `privateRegistry`, referenced by `publicRegistry`

### Converter
**Location:** `pkg/apiserver/conversion/conversion.go`

**Methods:**
- `PrivateToPublic(private) → public`
  - JSON round-trip automatically filters private-only fields
  - `filterPrivateMetadata()` removes labels/annotations with `private.orlop.gcp.managed.openshift.io/` prefix
  - `filterPrivateConditions()` removes conditions with `private.orlop.gcp.managed.openshift.io/` prefix

- `PublicToPrivate(public, existing) → private`
  - Starts with existing object to preserve internal fields
  - Overlays public data on top
  - Used for CREATE and UPDATE operations

### Schema Types

**Private Schema:**
```go
// All fields visible
type ObjectSpec struct {
    PublicField   string `json:"publicField"`
    InternalField string `json:"internalField"`  // Not tagged +orlop:public
}
```

**Public Schema:**
```go
// Only public fields
type ObjectSpec struct {
    PublicField string `json:"publicField"`
    // internalField omitted
}
```

### Router Setup

**Private Router:**
```go
privateRegistry := NewResourceRegistry(privateScheme)
privateRegistry.Register(privateResources...)
router := setupRouter(privateRegistry)  // Direct handlers
```

**Public Router:**
```go
publicRegistry := NewResourceRegistry(publicScheme)
publicRegistry.Register(publicResources...)

// Create converting handlers that:
//   - Use privateRegistry.GetStore() (shared storage)
//   - Use publicRegistry schemas (filtering)
//   - Use converter for type conversion
router := setupConvertingRouter(publicRegistry, privateRegistry, converter)
```

## Printer Columns (kubectl get)

### Overview
Orlop is **not** a CRD-based API server. It's an **aggregated API server** that:
- Private API: Uses genericapiserver (Kubernetes aggregation)
- Public API: Uses Chi router (custom REST handlers)
- Both expose resources without actual CRDs installed in the cluster

However, we still use kubebuilder's `+kubebuilder:printcolumn` markers for developer convenience and leverage controller-gen to extract metadata.

### Architecture

```text
┌─────────────────────────────────────────────────────────────────────┐
│ 1. Source: Go Type Definitions (e.g. cluster_types.go)             │
│    +kubebuilder:printcolumn:name="Available",type=string,...        │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 2. Code Generation (orlop-gen)                                      │
│    - Runs controller-gen crd:crdVersions=v1                         │
│    - Writes temporary CRD YAML files (Cluster.yaml, NodePool.yaml) │
│    - Extracts AdditionalPrinterColumns from CRD spec                │
│    - Embeds into ResourceInfo.PrinterColumns                        │
│    - Generates zz_generated.schemas.go                              │
│    - Deletes temporary CRD YAML files                               │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 3. Generated Code (zz_generated.schemas.go)                         │
│    ClusterResourceInfo = types.ResourceInfo{                        │
│      PrinterColumns: []types.PrinterColumn{                         │
│        {Name: "Available", Type: "string", Format: "",              │
│         JSONPath: `.status.conditions[?(@.type=="...")].status`},   │
│        {Name: "Age", Type: "date", Format: "date-time", ...},       │
│      },                                                              │
│    }                                                                 │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 4. Runtime: API Server (aggregated/storage.go)                      │
│    NewResourceStorage(..., printerColumns, ...)                     │
│    if len(printerColumns) > 0 {                                     │
│      convertor = NewCustomTableConvertor(gr, printerColumns)        │
│    }                                                                 │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 5. Table Conversion (aggregated/table.go)                           │
│    ConvertToTable(obj) → metav1.Table                               │
│    - Evaluates JSONPath using k8s.io/client-go/util/jsonpath        │
│    - Preserves native types (int, bool, time) via FindResults()     │
│    - Builds table with Name + custom columns                        │
└─────────────────────────────────────────────────────────────────────┘
```

### Key Files

**Generator:**
- `orlop/pkg/generator/schemas.go`: Runs controller-gen, extracts printer columns from temporary CRD YAML
- `orlop/pkg/apiserver/types/types.go`: `PrinterColumn` type with Name, Type, Format, JSONPath, Description, Priority

**Runtime:**
- `orlop/pkg/apiserver/aggregated/table.go`: `CustomTableConvertor` using k8s.io/client-go/util/jsonpath
- `orlop/pkg/apiserver/aggregated/storage.go`: Wires convertor into ResourceStorage when columns present

**Generated:**
- `platform-api/api/*/v1/zz_generated.schemas.go`: ResourceInfo with embedded PrinterColumns

### Important Notes

1. **No actual CRDs**: Controller-gen output is intermediate metadata only, never installed in cluster
2. **CRD YAML is temporary**: Generated during `make generate`, immediately parsed and deleted
3. **Format field**: Optional OpenAPI format modifier (int64, double, date-time). Currently only date columns get format auto-populated by controller-gen. For explicit formats on integer/number columns, add `format=int64` to kubebuilder marker when needed.
4. **JSONPath library**: Uses k8s.io/client-go/util/jsonpath (Kubernetes JSONPath dialect with filter support) instead of custom parser
5. **Type preservation**: JSONPath values returned via `FindResults()` preserve native Go types (int, bool, time.Time)

### Example

**Source:**
```go
// +kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Cluster struct {
    ...
}
```

**Generated CRD YAML (temporary):**
```yaml
spec:
  versions:
  - name: v1
    additionalPrinterColumns:
    - name: Available
      type: string
      jsonPath: .status.conditions[?(@.type=="Ready")].status
    - name: Age
      type: date
      jsonPath: .metadata.creationTimestamp
```

**Generated Go (permanent):**
```go
var ClusterResourceInfo = types.ResourceInfo{
    PrinterColumns: []types.PrinterColumn{
        {Name: "Available", Type: "string", JSONPath: `.status.conditions[?(@.type=="Ready")].status`},
        {Name: "Age", Type: "date", Format: "date-time", JSONPath: `.metadata.creationTimestamp`},
    },
}
```

**kubectl output:**
```text
NAME           AVAILABLE   AGE
my-cluster     True        2h
```

## Security Model

### Private API (Port 8080)
- Full access to all fields
- No filtering applied
- Intended for cluster-internal use
- Exposes `internalField`, private labels, private annotations, private conditions

### Public API (Port 8081)
- Filtered view of the same data
- Private fields automatically hidden via schema
- Private metadata explicitly filtered via converter
- Intended for external consumers
- Never exposes fields/metadata prefixed with `private.orlop.gcp.managed.openshift.io/`

## Benefits

1. **Single Source of Truth:** One storage backend prevents data inconsistency
2. **Automatic Filtering:** Schema + converter ensure private data stays private
3. **Preserved Semantics:** Public API updates preserve internal state
4. **Standard Kubernetes Patterns:** Uses standard GVK, resourceVersion, etc.
5. **Type Safety:** Separate schemes prevent accidental exposure
