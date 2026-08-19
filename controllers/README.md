# Gecko Controllers

The controllers module contains the Kubernetes-style reconciliation controllers that drive the Gecko lifecycle. Each controller is independently deployable as a subcommand of the `gecko-controllers` binary.

## Reconciliation Model

Gecko controllers follow the same reconciliation loop pattern used by Kubernetes itself, built on top of [controller-runtime](https://pkg.go.dev/sigs.k8s.io/controller-runtime):

1. **Watch** a resource type (e.g., `Cluster`, `NodePool`) for changes
2. **Receive** create/update/delete events as `reconcile.Request` objects
3. **Reconcile** — compare desired state (`.spec`) against current state (`.status`), take action to converge, and update status
4. **Requeue** to retry later if the desired state isn't yet reached

This approach is:
- **Declarative** — controllers react to desired state, not imperative commands
- **Level-triggered** — reconciliation is idempotent; it doesn't matter *what* changed, the controller always computes the full diff from current state
- **Eventually consistent** — if a reconcile fails, it requeues and retries until convergence

Gecko controllers use controller-runtime's `Manager`, `Reconciler` interface, and caching client to watch resources from the platform API, applying the same operator pattern used across the Kubernetes ecosystem.

## Architecture Overview

```mermaid
graph TD
    A[Cluster CR created] --> B[placement]
    A --> C[version-resolution]

    B -->|PlacementResult| D[hc-controller]
    C -->|VersionResolution| D

    E[NodePool CR created] --> G[nodepoolvrresolution]
    G -->|VersionResolution| F[nodepool-controller]
    F -.->|reads parent cluster status| B

    D -->|Apply| H[(Firestore)]
    F -->|Apply| H

    H <-->|Desires / Status| I[kube-applier-gcp]
    I <-->|Apply / Feedback| J[Management Cluster]

    H -.->|GetStatus| D
    H -.->|GetStatus| F

    style B fill:#4a90d9,color:#fff
    style C fill:#4a90d9,color:#fff
    style D fill:#e07b39,color:#fff
    style F fill:#e07b39,color:#fff
    style G fill:#4a90d9,color:#fff
    style H fill:#50b83c,color:#fff
    style I fill:#9b59b6,color:#fff
    style J fill:#9b59b6,color:#fff
```

## Controllers

### placement

Reconciles `Cluster` resources. Selects an eligible management cluster and DNS base domain for newly created clusters.

- Discovers management clusters dynamically from GCP Secret Manager (secrets labeled `mc-registration=true`)
- Filters by `mode=active`, caches results for 30s
- Round-robins across eligible MCs and configured DNS domains
- Writes `Status.PlacementResult` (ManagementClusterName, BaseDomain)

### version-resolution

Reconciles `Cluster` resources. Resolves the OCP release image for the requested version via the Cincinnati API.

- Queries the Cincinnati update graph for the matching release payload
- Writes `Status.VersionResolution` (ReleaseVersion, ReleaseImage, CincinnatiChannel)

### nodepool-vr (NodePool Version Resolution)

Reconciles `NodePool` resources. Same concept as version-resolution but for individual node pools.

### hc-controller (HostedCluster Controller)

Reconciles `Cluster` resources. Builds and applies HostedCluster manifests to the assigned management cluster.

**Readiness gates (must pass before applying):**
1. Placement ready (`Status.PlacementResult.ManagementClusterName` set)
2. Version resolution ready (`Status.VersionResolution` set)
3. Version match (resolved version matches `Spec.Release.Version`)

Builds HyperShift HostedCluster manifests from the cluster spec (GCP platform fields, workload identity, service accounts, networking) and applies them via the transport layer. Tracks `ResourcesApplied`, `HostedClusterAvailable`, and `ApiCertificateReady` conditions.

### nodepool-controller

Reconciles `NodePool` resources. Builds and applies NodePool manifests to the management cluster where the parent cluster is placed.

**Readiness gates:**
1. Cluster placement ready (reads parent cluster's `Status.PlacementResult`)
2. NodePool version resolution ready (`Status.VersionResolution` set)
3. Version match (resolved version matches `Spec.Release.Version`)

Tracks `NodePoolResourcesApplied`, `NodePoolAvailable`, and `NodePoolHealthy` conditions.

## Transport Layer

The transport layer abstracts how manifests are delivered to management clusters. Controllers never apply directly to MCs.

```mermaid
sequenceDiagram
    participant C as Controller
    participant FS as Firestore
    participant KA as kube-applier-gcp
    participant MC as Management Cluster

    C->>FS: Apply(manifests)
    Note over FS: ApplyDesire + ReadDesire docs
    KA->>FS: Poll for desires
    KA->>MC: Apply manifests
    MC-->>KA: Resource status
    KA-->>FS: Write status feedback
    C->>FS: GetStatus()
    FS-->>C: Conditions + resource status
```

The `transport.Client` interface supports:
- `Apply` — writes ApplyDesire + ReadDesire documents (manifests to apply and resources to monitor)
- `GetStatus` — reads feedback conditions and per-resource status
- `Delete` / `GetDeleteStatus` / `CleanupDeleteDesires` — async deletion flow

Implementations:
- **Firestore** (`client/transport/firestore/`) — production implementation using paired Firestore databases per MC
- **Mock** (`client/transport/mock/`) — in-memory implementation for unit tests

## Key Design Patterns

### Desire Documents
Controllers express intent (Apply/Read/Delete desires) in Firestore rather than applying directly. A separate `kube-applier-gcp` service on each MC processes these desires asynchronously. This decouples the control plane from management clusters.

### Finalizer-Based Async Deletion
1. `Delete()` enqueues DeleteDesire documents
2. Controller requeues, polling `GetDeleteStatus()` until all desires report success
3. `CleanupDeleteDesires()` removes the desire documents
4. Finalizer is removed, allowing Kubernetes garbage collection to complete

### Requeue Strategy
- **15 seconds** — while waiting for dependencies or async operations (pending state)
- **5 minutes** — after successful reconciliation (stable state)

## Directory Structure

```
controllers/
  cmd/                    # Subcommand entry points (one per controller)
  client/transport/       # Transport interface + Firestore/mock implementations
  hc/                     # HostedCluster controller + manifest building
  nodepool/               # NodePool controller + manifest building
  nodepoolvrresolution/   # NodePool version-resolution controller
  placement/              # Placement controller + dynamic MC selector
  versionresolution/      # Version-resolution controller + Cincinnati client
  util/                   # Shared packages (constants, errors, logger, setup)
  main.go                 # Root cobra command
```

## Running

```bash
# Build
make build

# Run a specific controller
./bin/gecko-controllers placement \
  --secretmanager-project my-gcp-project \
  --hc-dns-domains example.com,backup.example.com

./bin/gecko-controllers hc --log-level debug

# Common flags (all controllers)
--log-level {debug,info,warn,error}   # default: info
--log-format {json,text}              # default: json
--workers N                           # concurrent reconciliations, default: 10
--orlop-url URL                       # optional, uses in-cluster config if empty
```