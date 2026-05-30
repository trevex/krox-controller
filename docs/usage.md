# Using KroxDeployment

A `KroxDeployment` renders a KRO `ResourceGraphDefinition` (RGD) from a Flux
source and applies the resulting Kubernetes objects to the cluster, then
reconciles drift and prunes resources removed from the RGD.

## Prerequisites

- Flux `source-controller` installed and serving artifacts. `KroxDeployment`
  watches `GitRepository` and `OCIRepository` source CRs; install Flux
  source-controller first (see [fluxcd.io](https://fluxcd.io/)).
- `krox-controller` installed:
  ```
  make install               # apply CRDs
  make deploy IMG=<your-image>
  ```

## Minimal example

See `config/samples/krox_v1alpha1_kroxdeployment.yaml`.

```yaml
apiVersion: krox.io/v1alpha1
kind: KroxDeployment
metadata:
  name: webapp-prod
  namespace: apps
spec:
  interval: 5m
  sourceRef:
    kind: GitRepository
    name: blueprints
    namespace: flux-system
  path: ./rgds/webapp.yaml
  values:
    name: web
    replicas: 3
    image: nginx:1.27
  prune: true
```

The referenced source CR (`blueprints` in `flux-system`) is expected to expose
an artifact whose untarred tree contains the RGD at `./rgds/webapp.yaml`. The
controller fetches that tarball, evaluates the RGD against `spec.values`, and
applies the rendered resources.

## Conditions

| Condition    | Meaning                                                                        |
|--------------|--------------------------------------------------------------------------------|
| Ready        | Last reconcile succeeded                                                       |
| Reconciling  | A reconcile is in progress                                                     |
| Stalled      | Terminal failure; won't retry until source revision or KroxDeployment changes  |

Transient failures (source not ready, network errors, SSA conflicts) requeue
with backoff. Terminal failures (RGD parse error, schema mismatch, CEL invalid)
mark `Stalled=True` and pause retries until input changes.

## Pruning

With `spec.prune: true`, objects last applied by this `KroxDeployment` that are
absent from the new render are deleted with foreground propagation. The
controller tracks applied objects in `status.inventory.entries` (each entry is
a `<group>/<version>/<kind>/<namespace>/<name>` id plus the resourceVersion at
last apply).

When the source revision changes, the controller renders the new RGD, computes
the inventory diff, and deletes the objects that no longer appear in the new
render.

## Server-side apply ownership

Rendered objects are applied with field manager `krox-controller` and stamped
with annotation `krox.io/owned-by=<namespace>/<name>` (pointing back at the
owning `KroxDeployment`) and `krox.io/last-applied-revision=<source-revision>`.

Setting `spec.force: true` forces the controller to take field ownership on
SSA conflicts.

## Reconcile cadence

`spec.interval` controls how often the controller re-renders and reconciles.
The controller also watches the referenced `GitRepository` / `OCIRepository`
and re-enqueues on source events, so updates are typically picked up within a
few seconds of a new artifact revision being published — without waiting for
the next interval tick.

## Inspecting status

```
kubectl get krox -n apps
kubectl describe krox webapp-prod -n apps
```

The short name `krox` lets you list `KroxDeployment`s briefly. The
`printcolumn`s show the last applied revision and the Ready condition.
