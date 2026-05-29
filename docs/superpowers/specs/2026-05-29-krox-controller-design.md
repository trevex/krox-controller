# krox-controller — Design

**Date:** 2026-05-29
**Status:** Approved for planning

## Goal

A Flux-style Kubernetes controller that uses [KRO](https://kro.run/) `ResourceGraphDefinition`s (RGDs) as a *blueprinting deployment tool* — without requiring KRO to run in the cluster. The controller watches a `KroxDeployment` CR that references a Flux source (Git or OCI) holding an RGD, embeds KRO as a Go library to evaluate the RGD against user-supplied instance values, applies the resulting Kubernetes resources, and reconciles drift and removal.

Analogous to `flux-iac/tofu-controller` (Terraform as the renderer) but with KRO as the renderer.

## Non-goals (deferred)

- OCM-aware source discovery (the OCI source artifact is consumed as-is)
- Flux `Bucket` source kind
- Decryption (SOPS / age)
- `dependsOn` between `KroxDeployment`s
- Multi-RGD per source artifact
- HealthChecks beyond what KRO's `readyWhen` already provides
- Running a KRO controller in the cluster — KRO is purely a Go-library dependency

## Architecture overview

One controller binary, one CRD (`KroxDeployment`), one third-party runtime dependency in the cluster: Flux's `source-controller`.

```
┌─────────────────────────────────────────────────────────────────┐
│  krox-controller (controller-runtime manager)                   │
│                                                                 │
│  ┌──────────────────────┐    watches    ┌────────────────────┐  │
│  │ KroxDeployment       │◄──────────────│ source events      │  │
│  │   reconciler         │               │ (GitRepository,    │  │
│  └──────────┬───────────┘               │  OCIRepository)    │  │
│             │                            └────────────────────┘  │
│             ▼                                                    │
│  ┌──────────────────────┐                                        │
│  │ internal/source      │  fetch tarball, verify checksum,       │
│  │   → local tempdir    │  walk to spec.path → rgd.yaml          │
│  └──────────┬───────────┘                                        │
│             ▼                                                    │
│  ┌──────────────────────┐  graph.Builder.NewResourceGraphDef     │
│  │ internal/render      │  runtime.FromGraph                     │
│  │   layered evaluate   │  per DAG layer: GetDesired             │
│  └──────────┬───────────┘                                        │
│             ▼                                                    │
│  ┌──────────────────────┐  server-side apply, field manager      │
│  │ internal/apply       │  "krox-controller", owner label,       │
│  │   SSA + inventory    │  inventory diff & prune                │
│  └──────────────────────┘                                        │
└─────────────────────────────────────────────────────────────────┘
```

**Baked-in decisions:**
1. **KRO embedded as a library** — import `github.com/kubernetes-sigs/kro/pkg/graph` and `pkg/runtime`. Pin a version in `go.mod`. No fork.
2. **Layered apply** — RGD CEL expressions can reference upstream resources' `.status`, so the controller walks the DAG layer-by-layer, applying and observing before resolving the next layer.
3. **Server-side apply + inventory** — SSA with field manager `krox-controller`; inventory of applied objects persisted in `status.inventory`. Prune diffs old inventory vs new render.
4. **Flux source CRs are an import, not a vendor** — depend on `github.com/fluxcd/source-controller/api/v1` for the types. Users install Flux's `source-controller` as a prereq for actual artifact serving.
5. **Single namespaced CRD.** The RGD's outputs may still be cluster-scoped — those are tracked in the inventory the same way.

## API: `KroxDeployment` v1alpha1

Group `krox.io`, namespaced.

```go
type KroxDeploymentSpec struct {
    Interval  metav1.Duration       `json:"interval"`
    Suspend   bool                  `json:"suspend,omitempty"`
    SourceRef SourceReference       `json:"sourceRef"`
    Path      string                `json:"path"`
    // +kubebuilder:pruning:PreserveUnknownFields
    Values    *apiextensionsv1.JSON `json:"values,omitempty"`
    Prune     bool                  `json:"prune,omitempty"`
    Force     bool                  `json:"force,omitempty"` // force-take SSA conflicts
}

type SourceReference struct {
    // +kubebuilder:validation:Enum=GitRepository;OCIRepository
    Kind      string `json:"kind"`
    Name      string `json:"name"`
    Namespace string `json:"namespace,omitempty"`
}

type KroxDeploymentStatus struct {
    Conditions            []metav1.Condition  `json:"conditions,omitempty"`
    ObservedGeneration    int64               `json:"observedGeneration,omitempty"`
    LastAppliedRevision   string              `json:"lastAppliedRevision,omitempty"`
    LastAttemptedRevision string              `json:"lastAttemptedRevision,omitempty"`
    Inventory             *ResourceInventory  `json:"inventory,omitempty"`
}

type ResourceInventory struct {
    Entries []ResourceRef `json:"entries"`
}
type ResourceRef struct {
    // ID = "<group>/<version>/<kind>/<namespace>/<name>".
    // Cluster-scoped objects use an empty namespace component:
    //   "rbac.authorization.k8s.io/v1/ClusterRole//my-role"
    // Core group ("") yields a leading slash: "/v1/ConfigMap/apps/my-cm".
    ID      string `json:"id"`
    Version string `json:"v"` // resourceVersion at last apply (debug aid)
}
```

Conditions follow the Flux convention: `Ready`, `Reconciling`, `Stalled`.

Example CR:
```yaml
apiVersion: krox.io/v1alpha1
kind: KroxDeployment
metadata:
  name: webapp-prod
  namespace: apps
spec:
  interval: 5m
  sourceRef: { kind: GitRepository, name: blueprints, namespace: flux-system }
  path: ./rgds/webapp.yaml
  values:
    name: webapp
    replicas: 3
    image: nginx:1.27
  prune: true
```

## Reconcile flow

```
1. Read KroxDeployment; if suspend, exit. Set Reconciling=True.
2. Resolve source CR; if !Ready, requeue short.
3. Download artifact from source's status.artifact.url; verify checksum.
4. Untar to tempdir; read spec.path → rgd.yaml; unmarshal to kro v1alpha1.ResourceGraphDefinition.
5. builder := graph.NewBuilder(restConfig, httpClient)
   g, _ := builder.NewResourceGraphDefinition(rgd, RGDConfig{})
6. Build instance unstructured from RGD's GVK + spec.values; SetName/Namespace from KroxDeployment.
7. rt, _ := runtime.FromGraph(g, instance, RGDConfig{})
   for node in rt.Nodes() (topological order):
       desired := node.GetDesired()
       for each obj in desired:
           label krox.io/owned-by=<ns/name>
           annotate krox.io/last-applied-revision=<artifactRev>
           SSA apply (FieldOwner: krox-controller; force if spec.force)
           append to newInventory
       observed := liveGet(desired)
       node.SetObserved(observed)
       if node has readyWhen and !satisfied(observed):
           persist partial inventory; requeue short; exit
8. If spec.prune: delete (oldInventory \ newInventory) entries with foreground propagation.
9. Persist newInventory + lastAppliedRevision; Ready=True; requeue after spec.interval.
```

**Watches & enqueue:** `KroxDeployment` is primary; `GitRepository` and `OCIRepository` events are mapped via `EnqueueRequestsFromMapFunc` to re-enqueue every `KroxDeployment` referencing the changed source.

**Concurrency:** `MaxConcurrentReconciles` configurable (default 4). Each reconcile uses its own tempdir, cleaned via `defer`.

**Failure classification:**
- *Transient* → requeue with exponential backoff. Examples: artifact fetch, API throttling, SSA conflict (when `force=false`), `readyWhen` not satisfied yet.
- *Terminal* → `Stalled=True`; only retry on generation or source-revision change. Examples: RGD parse error, schema validation error, CEL compile error, type-mismatched values.

## Embedding KRO

```go
import (
    krov1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
    "github.com/kubernetes-sigs/kro/pkg/graph"
    "github.com/kubernetes-sigs/kro/pkg/runtime"
)

// Once per reconcile:
b, _ := graph.NewBuilder(restConfig, httpClient)
g, _ := b.NewResourceGraphDefinition(rgd, graph.RGDConfig{})
rt, _ := runtime.FromGraph(g, instanceUnstructured, graph.RGDConfig{})
for _, node := range rt.Nodes() {
    desired, _ := node.GetDesired()    // []*unstructured.Unstructured
    // apply...
    node.SetObserved(observedFromLive) // for downstream CEL refs
}
```

The Builder's schema resolver talks to the API server to fetch OpenAPI schemas for the **output resources** the RGD references (e.g., `apps/v1.Deployment`). It does not need KRO's `ResourceGraphDefinition` CRD installed in the cluster — the RGD is parsed from the source artifact directly into the Go struct.

## Testing strategy

Three layers, each guarding a distinct boundary.

### Unit tests (`go test ./...`, no cluster)
- `internal/render`: canned RGDs + values → assert produced unstructured objects, cover single-resource, cross-ref `${a.spec.x}`, `forEach`, `includeWhen`, invalid CEL (terminal error).
- `internal/source`: stub HTTP server returning fixture tarballs; assert checksum verify and path-traversal protection.
- `internal/apply`: fake dynamic client; assert SSA call shape, labels/annotations, inventory diff/prune logic.

### Controller tests (envtest, `make test`)
Spins up an `apiserver` + `etcd` via `setup-envtest`. Installs:
- Our `KroxDeployment` CRD
- Flux source CRDs (`GitRepository`, `OCIRepository`) — vendored at a pinned rev under `hack/vendored-crds/`

(KRO's CRD is **not** installed; not required at runtime.)

The suite uses **Ginkgo v2 + Gomega** (kubebuilder default). For each scenario:
1. Boot manager pointed at envtest.
2. Create a `GitRepository` CR with `status.artifact` pointing at an in-process HTTP fixture server.
3. Create a `KroxDeployment` referencing it.
4. Assert rendered objects appear, conditions transition, inventory populated, prune deletes when the RGD changes.

### E2E test (kind, `make test-e2e`)
- `kind create cluster` at a pinned k8s version.
- `make docker-build && kind load docker-image krox-controller:test`.
- Apply Flux `source-controller` (manifests pinned under `hack/vendored-crds/source-controller-install.yaml`).
- Apply krox-controller manager.
- Cases:
  1. RGD renders nginx Deployment+Service from a fixture tarball served as an `OCIRepository`; assert ready.
  2. Update RGD to drop the Service; assert it is pruned.
  3. Cross-resource expression: Deployment env references a ConfigMap value; assert correct value reaches the pod spec.
- Plain Go `testing.T` with `kubectl` subprocesses and `sigs.k8s.io/e2e-framework` helpers; defer Chainsaw to keep MVP dependencies tight.

Coverage targets: unit > 80% on `internal/{render,source,apply}`; controller test exercises happy path + 2–3 failure paths; e2e exercises 3 user-visible scenarios.

## Project layout

```
krox-controller/
├── api/v1alpha1/
│   ├── groupversion_info.go
│   ├── kroxdeployment_types.go
│   └── zz_generated.deepcopy.go
├── cmd/
│   └── main.go                       # manager bootstrap
├── internal/
│   ├── controller/
│   │   ├── kroxdeployment_controller.go
│   │   └── kroxdeployment_controller_test.go
│   ├── render/
│   │   ├── render.go
│   │   ├── instance.go
│   │   └── render_test.go
│   ├── source/
│   │   ├── fetch.go
│   │   ├── resolve.go
│   │   └── fetch_test.go
│   └── apply/
│       ├── applier.go
│       ├── inventory.go
│       └── applier_test.go
├── config/                           # kubebuilder kustomize layout
│   ├── crd/
│   ├── rbac/
│   ├── manager/
│   ├── samples/
│   └── default/
├── test/
│   ├── e2e/
│   │   ├── e2e_suite_test.go
│   │   ├── fixtures/
│   │   └── kroxdeployment_test.go
│   └── testdata/
│       └── rgds/
├── hack/
│   ├── boilerplate.go.txt
│   └── vendored-crds/                # flux source-controller CRDs pinned
├── Dockerfile
├── Makefile
├── PROJECT
├── go.mod
├── flake.nix
└── .envrc
```

Boundaries:

| Unit | Responsibility | Dependencies |
|---|---|---|
| `internal/render` | RGD + values → ordered layers of `*Unstructured` | `kro/pkg/graph`, `kro/pkg/runtime` |
| `internal/source` | sourceRef → fetched artifact at a local path | flux source v1 types, stdlib http |
| `internal/apply` | desired objects + old inventory → SSA + prune + new inventory | controller-runtime client |
| `internal/controller` | Wires the above as a reconciler | all of the above |

Each unit is testable independently: render with no cluster, source with a stub server, apply with a fake client. Only the controller test requires envtest.

## Nix devshell

Extend `flake.nix` with build, scaffold, and e2e tooling:

```nix
packages = with pkgs; [
  go gopls gotools go-tools             # existing

  kubebuilder                           # scaffold + Makefile templates
  controller-gen                        # CRD + deepcopy generation
  kustomize                             # config/ overlays

  golangci-lint
  gofumpt

  kind                                  # e2e cluster
  kubectl
];
```

`setup-envtest` is invoked via the Makefile on demand (it downloads pinned apiserver+etcd binaries to a local cache); if a nixpkgs `setup-envtest` package exists at implementation time, prefer adding it to the shell.

## Makefile targets

(Kubebuilder defaults plus:)
- `make manifests generate` — controller-gen
- `make test` — unit + envtest
- `make test-e2e` — kind + e2e
- `make docker-build docker-push`
- `make run` — runs controller against `$KUBECONFIG`
- `make install` — kubectl apply CRDs

## Open risks

- **KRO API stability.** `kro/pkg/graph` and `pkg/runtime` are not declared as stable libraries. We accept this and pin to a specific commit/tag in `go.mod`; upgrades require integration test re-validation.
- **Schema resolver requires API server access.** `graph.NewBuilder` needs a live `rest.Config` to fetch OpenAPI for output resource types. In tests we point it at envtest. In production it points at the local cluster. This is consistent with how KRO itself runs.
- **Layered apply latency.** A multi-layer RGD where intermediate resources take time to become Ready will cause the reconcile to exit and resume across multiple intervals. Acceptable; documented in user-facing docs as expected behavior.
