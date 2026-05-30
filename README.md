# krox-controller

> [!WARNING]
> This project was vibe coded to validate an idea and is not production-ready software!

A Kubernetes controller that renders [KRO](https://kro.run) `ResourceGraphDefinition`s
(RGDs) from Flux sources and applies the resulting objects to the cluster.

## Description

`krox-controller` bridges [Flux](https://fluxcd.io) and [KRO](https://kro.run):
it watches `GitRepository` / `OCIRepository` source CRs for RGD artifacts,
renders the RGD against per-instance values, and reconciles the resulting
Kubernetes objects via server-side apply.

The controller exposes a single CRD, `KroxDeployment` (short name `krox`), which
declares:

- a Flux `sourceRef` and `path` pointing at an RGD YAML inside the source artifact,
- `values` used to evaluate the RGD's spec schema and CEL expressions,
- an `interval` for periodic re-reconciliation (source events also re-enqueue),
- `prune` to garbage-collect objects no longer present in the latest render,
- `force` to take SSA field ownership on conflicts.

Status is reported via standard `Ready` / `Reconciling` / `Stalled` conditions
along with `lastAppliedRevision`, `observedGeneration`, and an `inventory` of
applied objects used for drift detection and pruning.

See [`docs/usage.md`](docs/usage.md) for a worked example and a description of
each condition, and `config/samples/` for a minimal CR.

## Getting Started

### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/krox-controller:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/krox-controller:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/krox-controller:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/krox-controller/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v2-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing

Issues and pull requests are welcome. Before opening a PR:

- Read [`AGENTS.md`](AGENTS.md) for the project layout, generated-file rules, and
  Kubebuilder conventions used here.
- Run `make manifests generate` after editing `*_types.go` or kubebuilder markers.
- Run `make lint-fix test` before pushing — unit tests use envtest (real
  API server + etcd) and must pass locally.
- Validate changes end-to-end against a [Kind](https://kind.sigs.k8s.io/) cluster
  with `make test-e2e`; do not run e2e against a shared dev/prod cluster.
- Keep changes focused and the commit history readable: one logical change per
  commit, conventional-style subject lines (`fix:`, `feat:`, …).

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2026 krox-controller authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

