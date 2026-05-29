# krox-controller Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Flux-style Kubernetes controller that watches `KroxDeployment` CRs, fetches a KRO `ResourceGraphDefinition` from a Flux source (Git/OCI), embeds KRO as a Go library to render it against user values, applies the resulting resources via server-side apply, and prunes drift.

**Architecture:** Single controller binary, single CRD (`KroxDeployment` v1alpha1 in group `krox.io`). KRO is imported as a Go library (`github.com/kubernetes-sigs/kro v0.9.2`) — never installed in-cluster. Flux `source-controller` (v1.8.5) is a runtime dependency for artifact serving but its types are imported as Go packages. Reconciler walks the RGD DAG layer-by-layer (KRO's CEL can reference upstream `.status`), server-side-applies each layer with field manager `krox-controller`, persists an inventory, and prunes on RGD changes.

**Tech Stack:**
- Go 1.26
- `sigs.k8s.io/controller-runtime` (kubebuilder-scaffolded)
- `github.com/kubernetes-sigs/kro v0.9.2` (graph + runtime packages)
- `github.com/fluxcd/source-controller/api/v1 v1.8.5`
- Ginkgo v2 + Gomega for envtest
- `sigs.k8s.io/e2e-framework` for e2e + kind
- Nix devshell

**Spec:** `docs/superpowers/specs/2026-05-29-krox-controller-design.md`

**Module path:** `github.com/trevex/krox-controller`

---

## File Structure

| Path | Responsibility |
|---|---|
| `api/v1alpha1/kroxdeployment_types.go` | CRD types: spec, status, conditions, inventory |
| `api/v1alpha1/groupversion_info.go` | Group + scheme registration (kubebuilder) |
| `internal/controller/kroxdeployment_controller.go` | Reconciler — orchestration only |
| `internal/controller/kroxdeployment_controller_test.go` | Ginkgo envtest suite |
| `internal/render/parser.go` | YAML → `kro v1alpha1.ResourceGraphDefinition` struct |
| `internal/render/instance.go` | Values → instance `*Unstructured` of the RGD's synthesized GVK |
| `internal/render/engine.go` | Thin wrappers around `graph.NewBuilder` / `runtime.FromGraph` |
| `internal/render/*_test.go` | Pure unit tests (no API server) |
| `internal/source/resolver.go` | `SourceReference` → `ArtifactInfo` via the cluster client |
| `internal/source/fetcher.go` | Download tarball → verify digest → untar to tempdir |
| `internal/source/*_test.go` | Stub HTTP server + tar fixture tests |
| `internal/apply/applier.go` | Server-side apply with field manager + owner labels |
| `internal/apply/inventory.go` | ID format, diff, parse |
| `internal/apply/pruner.go` | Delete missing entries via dynamic client |
| `internal/apply/*_test.go` | Fake dynamic client tests |
| `cmd/main.go` | Manager bootstrap (kubebuilder default + our wiring) |
| `hack/vendored-crds/gitrepositories.yaml` | Flux GitRepository CRD pinned at v1.8.5 |
| `hack/vendored-crds/ocirepositories.yaml` | Flux OCIRepository CRD pinned at v1.8.5 |
| `hack/vendored-crds/source-controller-install.yaml` | Full Flux source-controller install for e2e |
| `config/samples/krox_v1alpha1_kroxdeployment.yaml` | Example user CR |
| `test/e2e/` | kind-based e2e suite |
| `test/testdata/rgds/` | RGD fixtures used by tests |
| `flake.nix` | Nix devshell with build/test/e2e tooling |
| `Makefile` | Standard kubebuilder targets + e2e |

---

## Task 1: Bootstrap project — devshell, kubebuilder init, dependencies

**Files:**
- Modify: `flake.nix`
- Create (via kubebuilder): `PROJECT`, `Dockerfile`, `Makefile`, `go.mod`, `cmd/main.go`, `config/**`, `hack/boilerplate.go.txt`
- Modify: `go.mod` (after kubebuilder init, add KRO + Flux deps)
- Create: `hack/vendored-crds/gitrepositories.yaml`
- Create: `hack/vendored-crds/ocirepositories.yaml`

- [ ] **Step 1.1: Extend the nix devshell**

Replace `flake.nix` with:

```nix
{
  description = "krox-controller: KRO-as-library Flux-style deployment controller";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        goOverlay = final: prev: { go = prev.go_1_26; };
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ goOverlay ];
        };
      in {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go gopls gotools go-tools
            kubebuilder
            kubernetes-controller-tools  # controller-gen
            kustomize
            golangci-lint
            gofumpt
            kind
            kubectl
          ];
          shellHook = ''
            echo "Go      $(go version | cut -d' ' -f3)"
            echo "kubebuilder $(kubebuilder version 2>&1 | head -1 || echo n/a)"
            echo "kind        $(kind version 2>&1 | head -1)"
          '';
        };
      });
}
```

- [ ] **Step 1.2: Reload devshell and verify**

Run:
```
direnv reload   # or: nix develop
which kubebuilder && which controller-gen && which kind
```
Expected: all three resolve. If `kubernetes-controller-tools` package name differs in current nixpkgs, substitute (`controller-gen` may be exposed as a separate attribute) — the engineer should resolve at this step before continuing.

- [ ] **Step 1.3: Run kubebuilder init**

```
kubebuilder init \
  --domain krox.io \
  --repo github.com/trevex/krox-controller \
  --owner "krox-controller authors" \
  --license apache2
```
Expected: kubebuilder creates `PROJECT`, `cmd/main.go`, `Dockerfile`, `Makefile`, `go.mod`, `config/**`, `hack/boilerplate.go.txt`. No errors.

- [ ] **Step 1.4: Scaffold the API**

```
kubebuilder create api \
  --group core \
  --version v1alpha1 \
  --kind KroxDeployment \
  --resource --controller
```
Expected: `api/v1alpha1/kroxdeployment_types.go`, `internal/controller/kroxdeployment_controller.go`, plus CRD/test scaffolds. Prompts: answer "y" to both "Create Resource" and "Create Controller".

Then fix the API group in `PROJECT` and `api/v1alpha1/groupversion_info.go` if kubebuilder used `core.krox.io` (we want just `krox.io`). Edit `api/v1alpha1/groupversion_info.go`:

```go
var GroupVersion = schema.GroupVersion{Group: "krox.io", Version: "v1alpha1"}
```

And update `PROJECT` similarly so subsequent `make manifests` is correct.

- [ ] **Step 1.5: (Deferred — see note)**

Originally this step ran `go get` for KRO, Flux source, and e2e-framework. In practice these deps cannot survive `go mod tidy` until production code imports them, and a `hack/tools.go` build-tag workaround causes gopls to flag missing transitive `go.sum` entries. Deps are added with the tasks that introduce the imports:

- KRO (`github.com/kubernetes-sigs/kro v0.9.2`) → Task 4 (`internal/render/parser.go`)
- Flux source API (`github.com/fluxcd/source-controller/api v1.8.5`) → Task 7 (`internal/source/resolver.go`)
- e2e-framework (`sigs.k8s.io/e2e-framework`) → Task 13 (`test/e2e/e2e_suite_test.go`)

No action needed here.

- [ ] **Step 1.6: Vendor Flux source CRDs for envtest**

```
mkdir -p hack/vendored-crds
curl -fsSL -o hack/vendored-crds/gitrepositories.yaml \
  https://raw.githubusercontent.com/fluxcd/source-controller/v1.8.5/config/crd/bases/source.toolkit.fluxcd.io_gitrepositories.yaml
curl -fsSL -o hack/vendored-crds/ocirepositories.yaml \
  https://raw.githubusercontent.com/fluxcd/source-controller/v1.8.5/config/crd/bases/source.toolkit.fluxcd.io_ocirepositories.yaml
```
Expected: two non-empty YAML files starting with `apiVersion: apiextensions.k8s.io/v1`.

- [ ] **Step 1.7: Build to verify the skeleton compiles**

```
go build ./...
```
Expected: no errors.

- [ ] **Step 1.8: Commit**

```
git add flake.nix PROJECT Dockerfile Makefile go.mod go.sum cmd/ api/ internal/ config/ hack/ .gitignore
git commit -m "Bootstrap kubebuilder project with KRO and Flux source dependencies"
```

---

## Task 2: Define `KroxDeployment` v1alpha1 types

**Files:**
- Modify: `api/v1alpha1/kroxdeployment_types.go`
- Test: `api/v1alpha1/kroxdeployment_types_test.go`

- [ ] **Step 2.1: Write a roundtrip test for the types**

Create `api/v1alpha1/kroxdeployment_types_test.go`:

```go
package v1alpha1

import (
	"encoding/json"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestKroxDeploymentRoundtrip(t *testing.T) {
	in := KroxDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "y"},
		Spec: KroxDeploymentSpec{
			Interval: metav1.Duration{Duration: 5 * 60 * 1_000_000_000}, // 5m
			SourceRef: SourceReference{
				Kind: "GitRepository", Name: "src", Namespace: "flux-system",
			},
			Path:   "./rgd.yaml",
			Values: &apiextensionsv1.JSON{Raw: []byte(`{"replicas":3}`)},
			Prune:  true,
			Force:  false,
		},
		Status: KroxDeploymentStatus{
			ObservedGeneration:    1,
			LastAppliedRevision:   "sha:abc",
			LastAttemptedRevision: "sha:def",
			Inventory: &ResourceInventory{
				Entries: []ResourceRef{
					{ID: "apps/v1/Deployment/y/app", Version: "100"},
				},
			},
		},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out KroxDeployment
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Spec.Path != "./rgd.yaml" {
		t.Fatalf("path: %q", out.Spec.Path)
	}
	if out.Status.Inventory.Entries[0].ID != "apps/v1/Deployment/y/app" {
		t.Fatalf("inventory: %+v", out.Status.Inventory)
	}
}
```

- [ ] **Step 2.2: Run test to verify it fails**

```
go test ./api/v1alpha1/ -run TestKroxDeploymentRoundtrip -v
```
Expected: FAIL — `KroxDeploymentSpec` is the scaffolded empty struct.

- [ ] **Step 2.3: Replace `api/v1alpha1/kroxdeployment_types.go`**

Replace its contents with:

```go
// +groupName=krox.io
package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KroxDeploymentSpec defines a KRO RGD render+apply unit.
type KroxDeploymentSpec struct {
	// Interval is how often the controller re-renders and applies.
	// +required
	Interval metav1.Duration `json:"interval"`

	// Suspend pauses reconciliation when true.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// SourceRef points at a Flux source CR containing the RGD artifact.
	// +required
	SourceRef SourceReference `json:"sourceRef"`

	// Path is the location of the RGD YAML inside the source artifact.
	// +required
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`

	// Values are the instance values for the RGD. Validated at render time
	// against the RGD's spec schema.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Values *apiextensionsv1.JSON `json:"values,omitempty"`

	// Prune deletes owned objects no longer present in the latest render.
	// +optional
	Prune bool `json:"prune,omitempty"`

	// Force forces server-side-apply to take ownership on conflicts.
	// +optional
	Force bool `json:"force,omitempty"`
}

// SourceReference identifies a Flux source CR.
type SourceReference struct {
	// +kubebuilder:validation:Enum=GitRepository;OCIRepository
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// KroxDeploymentStatus reports the last reconcile outcome.
type KroxDeploymentStatus struct {
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	LastAppliedRevision string `json:"lastAppliedRevision,omitempty"`
	// +optional
	LastAttemptedRevision string `json:"lastAttemptedRevision,omitempty"`
	// +optional
	Inventory *ResourceInventory `json:"inventory,omitempty"`
}

// ResourceInventory lists objects last applied by a KroxDeployment.
type ResourceInventory struct {
	Entries []ResourceRef `json:"entries"`
}

// ResourceRef is a stable identifier for an applied object.
// ID = "<group>/<version>/<kind>/<namespace>/<name>"; cluster-scoped objects
// use an empty namespace component (e.g. "rbac.authorization.k8s.io/v1/ClusterRole//x");
// core group "" yields a leading slash (e.g. "/v1/ConfigMap/ns/x").
type ResourceRef struct {
	ID      string `json:"id"`
	Version string `json:"v"`
}

// Condition types.
const (
	ConditionReady       = "Ready"
	ConditionReconciling = "Reconciling"
	ConditionStalled     = "Stalled"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=krox
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.status.lastAppliedRevision`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KroxDeployment is a KRO RGD applied to the cluster.
type KroxDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KroxDeploymentSpec   `json:"spec,omitempty"`
	Status            KroxDeploymentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KroxDeploymentList is a list of KroxDeployment.
type KroxDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KroxDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KroxDeployment{}, &KroxDeploymentList{})
}
```

- [ ] **Step 2.4: Regenerate deepcopy + CRD manifests**

```
make manifests generate
```
Expected: `api/v1alpha1/zz_generated.deepcopy.go` updated; `config/crd/bases/krox.io_kroxdeployments.yaml` exists and contains `spec.interval`, `spec.sourceRef`, `status.inventory`.

- [ ] **Step 2.5: Run the roundtrip test**

```
go test ./api/v1alpha1/ -v
```
Expected: PASS.

- [ ] **Step 2.6: Commit**

```
git add api/ config/
git commit -m "Add KroxDeployment v1alpha1 types and CRD manifest"
```

---

## Task 3: Inventory ID/diff helpers

**Files:**
- Create: `internal/apply/inventory.go`
- Create: `internal/apply/inventory_test.go`

- [ ] **Step 3.1: Write failing tests**

Create `internal/apply/inventory_test.go`:

```go
package apply

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	v1alpha1 "github.com/trevex/krox-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestIDFromObject(t *testing.T) {
	cases := []struct {
		name string
		gvk  schema.GroupVersionKind
		ns   string
		obj  string
		want string
	}{
		{"core ns", schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, "apps", "cm", "/v1/ConfigMap/apps/cm"},
		{"apps", schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, "apps", "d", "apps/v1/Deployment/apps/d"},
		{"cluster-scoped", schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}, "", "r", "rbac.authorization.k8s.io/v1/ClusterRole//r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(tc.gvk)
			u.SetNamespace(tc.ns)
			u.SetName(tc.obj)
			if got := IDFromObject(u); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestParseID(t *testing.T) {
	gvk, ns, name, err := ParseID("apps/v1/Deployment/team/web")
	if err != nil {
		t.Fatal(err)
	}
	if gvk.Group != "apps" || gvk.Version != "v1" || gvk.Kind != "Deployment" {
		t.Fatalf("gvk: %+v", gvk)
	}
	if ns != "team" || name != "web" {
		t.Fatalf("ns/name: %q/%q", ns, name)
	}
	if _, _, _, err := ParseID("bad"); err == nil {
		t.Fatal("expected error on malformed id")
	}
}

func TestDiff(t *testing.T) {
	old := &v1alpha1.ResourceInventory{Entries: []v1alpha1.ResourceRef{
		{ID: "apps/v1/Deployment/n/a"},
		{ID: "/v1/ConfigMap/n/x"},
		{ID: "/v1/Service/n/svc"},
	}}
	cur := &v1alpha1.ResourceInventory{Entries: []v1alpha1.ResourceRef{
		{ID: "apps/v1/Deployment/n/a"},
		{ID: "/v1/ConfigMap/n/x"},
	}}
	got := Diff(old, cur)
	want := []v1alpha1.ResourceRef{{ID: "/v1/Service/n/svc"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("Diff mismatch (-want +got):\n%s", diff)
	}

	// nil old → nothing to delete.
	if got := Diff(nil, cur); len(got) != 0 {
		t.Fatalf("nil old: %+v", got)
	}
}
```

Add the go-cmp dep:
```
go get github.com/google/go-cmp/cmp
```

- [ ] **Step 3.2: Run test to verify it fails**

```
go test ./internal/apply/ -v
```
Expected: FAIL — package does not compile (functions undefined).

- [ ] **Step 3.3: Implement `internal/apply/inventory.go`**

```go
package apply

import (
	"fmt"
	"strings"

	v1alpha1 "github.com/trevex/krox-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// IDFromObject builds a stable identifier "<g>/<v>/<k>/<ns>/<name>".
// Cluster-scoped objects use an empty namespace component.
func IDFromObject(u *unstructured.Unstructured) string {
	gvk := u.GroupVersionKind()
	return fmt.Sprintf("%s/%s/%s/%s/%s", gvk.Group, gvk.Version, gvk.Kind, u.GetNamespace(), u.GetName())
}

// ParseID splits an inventory ID into its components.
func ParseID(id string) (schema.GroupVersionKind, string, string, error) {
	parts := strings.SplitN(id, "/", 5)
	if len(parts) != 5 {
		return schema.GroupVersionKind{}, "", "", fmt.Errorf("malformed inventory id %q (want g/v/k/ns/name)", id)
	}
	return schema.GroupVersionKind{
		Group:   parts[0],
		Version: parts[1],
		Kind:    parts[2],
	}, parts[3], parts[4], nil
}

// Diff returns entries present in old but missing from cur.
func Diff(old, cur *v1alpha1.ResourceInventory) []v1alpha1.ResourceRef {
	if old == nil {
		return nil
	}
	curSet := map[string]struct{}{}
	if cur != nil {
		for _, r := range cur.Entries {
			curSet[r.ID] = struct{}{}
		}
	}
	var out []v1alpha1.ResourceRef
	for _, r := range old.Entries {
		if _, found := curSet[r.ID]; !found {
			out = append(out, r)
		}
	}
	return out
}
```

- [ ] **Step 3.4: Run test to verify it passes**

```
go test ./internal/apply/ -v
```
Expected: all three tests PASS.

- [ ] **Step 3.5: Commit**

```
git add internal/apply/ go.mod go.sum
git commit -m "Add inventory ID/diff helpers"
```

---

## Task 4: RGD parser

**Files:**
- Create: `internal/render/parser.go`
- Create: `internal/render/parser_test.go`
- Create: `test/testdata/rgds/webapp.yaml`

- [ ] **Step 4.1: Create RGD fixture**

`test/testdata/rgds/webapp.yaml`:

```yaml
apiVersion: kro.run/v1alpha1
kind: ResourceGraphDefinition
metadata:
  name: webapp
spec:
  schema:
    apiVersion: v1alpha1
    kind: WebApp
    spec:
      name: string
      replicas: integer | default=1
      image: string
  resources:
    - id: deployment
      template:
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: ${schema.spec.name}
        spec:
          replicas: ${schema.spec.replicas}
          selector: { matchLabels: { app: ${schema.spec.name} } }
          template:
            metadata: { labels: { app: ${schema.spec.name} } }
            spec:
              containers:
                - name: app
                  image: ${schema.spec.image}
```

- [ ] **Step 4.2: Write a failing test**

`internal/render/parser_test.go`:

```go
package render

import (
	"os"
	"testing"
)

func TestParseRGD(t *testing.T) {
	data, err := os.ReadFile("../../test/testdata/rgds/webapp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rgd, err := ParseRGD(data)
	if err != nil {
		t.Fatalf("ParseRGD: %v", err)
	}
	if rgd.Name != "webapp" {
		t.Fatalf("name: %q", rgd.Name)
	}
	if rgd.Spec.Schema.Kind != "WebApp" {
		t.Fatalf("kind: %q", rgd.Spec.Schema.Kind)
	}
	if len(rgd.Spec.Resources) != 1 {
		t.Fatalf("resources: %d", len(rgd.Spec.Resources))
	}
	if rgd.Spec.Resources[0].ID != "deployment" {
		t.Fatalf("resource id: %q", rgd.Spec.Resources[0].ID)
	}

	if _, err := ParseRGD([]byte("not yaml: : :")); err == nil {
		t.Fatal("expected error on bad yaml")
	}
}
```

- [ ] **Step 4.3: Run test to verify it fails**

```
go test ./internal/render/ -v
```
Expected: FAIL — package does not compile.

- [ ] **Step 4.4: Implement `internal/render/parser.go`**

```go
package render

import (
	"fmt"

	krov1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// ParseRGD decodes a single ResourceGraphDefinition from a YAML document.
func ParseRGD(data []byte) (*krov1.ResourceGraphDefinition, error) {
	rgd := &krov1.ResourceGraphDefinition{}
	if err := yaml.Unmarshal(data, rgd); err != nil {
		return nil, fmt.Errorf("decode RGD: %w", err)
	}
	if rgd.Spec.Schema.Kind == "" {
		return nil, fmt.Errorf("RGD %q missing spec.schema.kind", rgd.Name)
	}
	if len(rgd.Spec.Resources) == 0 {
		return nil, fmt.Errorf("RGD %q has no resources", rgd.Name)
	}
	return rgd, nil
}
```

- [ ] **Step 4.5: Run test to verify it passes**

```
go test ./internal/render/ -v -run TestParseRGD
```
Expected: PASS.

- [ ] **Step 4.6: Commit**

```
git add internal/render/parser.go internal/render/parser_test.go test/testdata/
git commit -m "Add RGD YAML parser"
```

---

## Task 5: Instance builder

**Files:**
- Create: `internal/render/instance.go`
- Create: `internal/render/instance_test.go`

- [ ] **Step 5.1: Write failing test**

`internal/render/instance_test.go`:

```go
package render

import (
	"os"
	"testing"
)

func TestBuildInstance(t *testing.T) {
	data, _ := os.ReadFile("../../test/testdata/rgds/webapp.yaml")
	rgd, _ := ParseRGD(data)

	inst, err := BuildInstance(rgd, "myapp", "production", []byte(`{"name":"web","replicas":3,"image":"nginx:1.27"}`))
	if err != nil {
		t.Fatalf("BuildInstance: %v", err)
	}

	gvk := inst.GroupVersionKind()
	// kro.run/v1alpha1 group is fixed for RGD instance kinds.
	if gvk.Kind != "WebApp" {
		t.Fatalf("kind %q", gvk.Kind)
	}
	if inst.GetName() != "myapp" {
		t.Fatalf("name %q", inst.GetName())
	}
	if inst.GetNamespace() != "production" {
		t.Fatalf("ns %q", inst.GetNamespace())
	}
	spec, _, _ := unstructuredSpec(t, inst)
	if spec["name"] != "web" || spec["image"] != "nginx:1.27" {
		t.Fatalf("spec: %+v", spec)
	}

	// nil values produces empty spec.
	inst2, err := BuildInstance(rgd, "x", "ns", nil)
	if err != nil {
		t.Fatalf("nil values: %v", err)
	}
	s, ok, _ := unstructuredSpec(t, inst2)
	if !ok || len(s) != 0 {
		t.Fatalf("expected empty spec, got %+v ok=%v", s, ok)
	}
}

func unstructuredSpec(t *testing.T, u interface{ UnstructuredContent() map[string]any }) (map[string]any, bool, error) {
	t.Helper()
	obj := u.UnstructuredContent()
	spec, ok := obj["spec"].(map[string]any)
	return spec, ok, nil
}
```

- [ ] **Step 5.2: Run test to verify it fails**

```
go test ./internal/render/ -v -run TestBuildInstance
```
Expected: FAIL — `BuildInstance` undefined.

- [ ] **Step 5.3: Implement `internal/render/instance.go`**

```go
package render

import (
	"encoding/json"
	"fmt"

	krov1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// BuildInstance builds an *unstructured.Unstructured of the RGD's synthesized
// GVK with the provided values placed under spec. The instance kind is taken
// from the RGD's schema; the apiVersion is forced to "kro.run/v1alpha1" so
// downstream KRO machinery can match its schema cache.
func BuildInstance(rgd *krov1.ResourceGraphDefinition, name, namespace string, values []byte) (*unstructured.Unstructured, error) {
	spec := map[string]any{}
	if len(values) > 0 {
		if err := json.Unmarshal(values, &spec); err != nil {
			return nil, fmt.Errorf("decode values: %w", err)
		}
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kro.run",
		Version: "v1alpha1",
		Kind:    rgd.Spec.Schema.Kind,
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	u.Object["spec"] = spec
	return u, nil
}
```

- [ ] **Step 5.4: Run test to verify it passes**

```
go test ./internal/render/ -v
```
Expected: both tests PASS.

- [ ] **Step 5.5: Commit**

```
git add internal/render/instance.go internal/render/instance_test.go
git commit -m "Add instance builder"
```

---

## Task 6: Render engine wrappers

**Files:**
- Create: `internal/render/engine.go`

(Engine wrappers require a live `rest.Config` because KRO's schema resolver queries the API server. Real end-to-end tests for the engine live at the envtest/e2e layer; this task only provides thin wrappers.)

- [ ] **Step 6.1: Implement `internal/render/engine.go`**

```go
package render

import (
	"fmt"
	"net/http"

	krov1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"github.com/kubernetes-sigs/kro/pkg/graph"
	kroruntime "github.com/kubernetes-sigs/kro/pkg/runtime"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

// Engine builds KRO runtime instances. It is safe for concurrent use; the
// underlying graph.Builder caches OpenAPI schemas keyed by GVK.
type Engine struct {
	builder *graph.Builder
}

// NewEngine constructs an Engine backed by the given rest.Config + http.Client.
// httpClient may be nil; the Builder will create a default client.
func NewEngine(cfg *rest.Config, httpClient *http.Client) (*Engine, error) {
	b, err := graph.NewBuilder(cfg, httpClient)
	if err != nil {
		return nil, fmt.Errorf("new graph builder: %w", err)
	}
	return &Engine{builder: b}, nil
}

// Plan parses+validates the RGD and returns a runtime ready to render against
// the instance. Errors here are terminal (schema or CEL validation failures).
func (e *Engine) Plan(rgd *krov1.ResourceGraphDefinition, instance *unstructured.Unstructured) (*kroruntime.Runtime, error) {
	g, err := e.builder.NewResourceGraphDefinition(rgd, graph.RGDConfig{})
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}
	rt, err := kroruntime.FromGraph(g, instance, graph.RGDConfig{})
	if err != nil {
		return nil, fmt.Errorf("init runtime: %w", err)
	}
	return rt, nil
}
```

- [ ] **Step 6.2: Verify compile**

```
go build ./internal/render/...
```
Expected: no errors.

- [ ] **Step 6.3: Commit**

```
git add internal/render/engine.go
git commit -m "Add KRO graph+runtime wrapper engine"
```

---

## Task 7: Source resolver

**Files:**
- Create: `internal/source/resolver.go`
- Create: `internal/source/resolver_test.go`

- [ ] **Step 7.1: Write failing test**

`internal/source/resolver_test.go`:

```go
package source

import (
	"context"
	"testing"
	"time"

	srcv1 "github.com/fluxcd/source-controller/api/v1"
	v1alpha1 "github.com/trevex/krox-controller/api/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := runtime.NewScheme()
	if err := srcv1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	return sch
}

func TestResolveGitRepository(t *testing.T) {
	sch := newScheme(t)
	gr := &srcv1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "src", Namespace: "flux-system"},
		Status: srcv1.GitRepositoryStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, LastTransitionTime: metav1.Now()}},
			Artifact: &srcv1.Artifact{
				URL:            "http://src.example/x.tgz",
				Revision:       "main@sha1:abc",
				Digest:         "sha256:deadbeef",
				LastUpdateTime: metav1.NewTime(time.Now()),
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(gr).WithStatusSubresource(gr).Build()
	r := &Resolver{Client: c}

	got, err := r.Resolve(context.Background(), "apps", v1alpha1.SourceReference{
		Kind: "GitRepository", Name: "src", Namespace: "flux-system",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.URL != gr.Status.Artifact.URL || got.Digest != "sha256:deadbeef" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveNotReady(t *testing.T) {
	sch := newScheme(t)
	gr := &srcv1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "src", Namespace: "flux-system"},
		Status: srcv1.GitRepositoryStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, LastTransitionTime: metav1.Now()}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(gr).WithStatusSubresource(gr).Build()
	r := &Resolver{Client: c}

	_, err := r.Resolve(context.Background(), "apps", v1alpha1.SourceReference{
		Kind: "GitRepository", Name: "src", Namespace: "flux-system",
	})
	if !IsNotReady(err) {
		t.Fatalf("expected NotReady, got %v", err)
	}
}

func TestResolveDefaultsNamespace(t *testing.T) {
	sch := newScheme(t)
	gr := &srcv1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "src", Namespace: "apps"},
		Status: srcv1.GitRepositoryStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}},
			Artifact:   &srcv1.Artifact{URL: "u", Digest: "d"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(gr).Build()
	r := &Resolver{Client: c}
	got, err := r.Resolve(context.Background(), "apps", v1alpha1.SourceReference{Kind: "GitRepository", Name: "src"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.URL != "u" {
		t.Fatalf("got %+v", got)
	}
	// silence apimeta unused import in some Go versions
	_ = apimeta.IsNoMatchError
}
```

- [ ] **Step 7.2: Run test to verify it fails**

```
go test ./internal/source/ -v
```
Expected: FAIL — package doesn't compile.

- [ ] **Step 7.3: Implement `internal/source/resolver.go`**

```go
package source

import (
	"context"
	"errors"
	"fmt"

	srcv1 "github.com/fluxcd/source-controller/api/v1"
	v1alpha1 "github.com/trevex/krox-controller/api/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ArtifactInfo describes a Flux artifact ready to be fetched.
type ArtifactInfo struct {
	URL      string
	Revision string
	Digest   string
}

// Resolver reads Flux source CRs and extracts artifact metadata.
type Resolver struct {
	Client client.Client
}

// notReadyError marks a transient source-not-ready condition.
type notReadyError struct{ msg string }

func (e *notReadyError) Error() string { return e.msg }

// IsNotReady reports whether err is a transient source-not-ready error.
func IsNotReady(err error) bool {
	var nr *notReadyError
	return errors.As(err, &nr)
}

// Resolve loads the referenced source CR and returns its artifact info.
// parentNamespace is used when SourceReference.Namespace is empty.
func (r *Resolver) Resolve(ctx context.Context, parentNamespace string, ref v1alpha1.SourceReference) (*ArtifactInfo, error) {
	ns := ref.Namespace
	if ns == "" {
		ns = parentNamespace
	}
	key := types.NamespacedName{Namespace: ns, Name: ref.Name}

	switch ref.Kind {
	case "GitRepository":
		var gr srcv1.GitRepository
		if err := r.Client.Get(ctx, key, &gr); err != nil {
			return nil, fmt.Errorf("get GitRepository %s: %w", key, err)
		}
		return artifactFromConditions(gr.Status.Conditions, gr.Status.Artifact, key)
	case "OCIRepository":
		var oci srcv1.OCIRepository
		if err := r.Client.Get(ctx, key, &oci); err != nil {
			return nil, fmt.Errorf("get OCIRepository %s: %w", key, err)
		}
		return artifactFromConditions(oci.Status.Conditions, oci.Status.Artifact, key)
	default:
		return nil, fmt.Errorf("unsupported source kind %q", ref.Kind)
	}
}

func artifactFromConditions(conds []metav1.Condition, art *srcv1.Artifact, key types.NamespacedName) (*ArtifactInfo, error) {
	ready := apimeta.FindStatusCondition(conds, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue {
		return nil, &notReadyError{msg: fmt.Sprintf("source %s not Ready", key)}
	}
	if art == nil || art.URL == "" {
		return nil, &notReadyError{msg: fmt.Sprintf("source %s has no artifact", key)}
	}
	return &ArtifactInfo{URL: art.URL, Revision: art.Revision, Digest: art.Digest}, nil
}
```

- [ ] **Step 7.4: Run test to verify it passes**

```
go test ./internal/source/ -v
```
Expected: all three tests PASS.

- [ ] **Step 7.5: Commit**

```
git add internal/source/resolver.go internal/source/resolver_test.go
git commit -m "Add Flux source resolver"
```

---

## Task 8: Tarball fetcher

**Files:**
- Create: `internal/source/fetcher.go`
- Create: `internal/source/fetcher_test.go`

- [ ] **Step 8.1: Write failing test**

`internal/source/fetcher_test.go`:

```go
package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func makeTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestFetchSuccess(t *testing.T) {
	data := makeTarGz(t, map[string]string{"rgd.yaml": "kind: RGD\n"})
	digest := "sha256:" + sha256Hex(data)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := &Fetcher{HTTPClient: srv.Client()}
	if err := f.Fetch(context.Background(), ArtifactInfo{URL: srv.URL, Digest: digest}, dir); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "rgd.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "kind: RGD\n" {
		t.Fatalf("contents: %q", string(got))
	}
}

func TestFetchDigestMismatch(t *testing.T) {
	data := makeTarGz(t, map[string]string{"x.txt": "x"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	}))
	defer srv.Close()
	dir := t.TempDir()
	f := &Fetcher{HTTPClient: srv.Client()}
	err := f.Fetch(context.Background(), ArtifactInfo{URL: srv.URL, Digest: "sha256:0000"}, dir)
	if err == nil || !contains(err.Error(), "digest") {
		t.Fatalf("expected digest error, got %v", err)
	}
}

func TestFetchPathTraversal(t *testing.T) {
	data := makeTarGz(t, map[string]string{"../escape": "no"})
	digest := "sha256:" + sha256Hex(data)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	}))
	defer srv.Close()
	dir := t.TempDir()
	f := &Fetcher{HTTPClient: srv.Client()}
	err := f.Fetch(context.Background(), ArtifactInfo{URL: srv.URL, Digest: digest}, dir)
	if err == nil || !contains(err.Error(), "path traversal") {
		t.Fatalf("expected path traversal error, got %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (indexOf(s, sub) >= 0))
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// suppress unused imports if Go optimizes them
var _ = fmt.Sprintf
```

- [ ] **Step 8.2: Run test to verify it fails**

```
go test ./internal/source/ -v -run TestFetch
```
Expected: FAIL — `Fetcher` undefined.

- [ ] **Step 8.3: Implement `internal/source/fetcher.go`**

```go
package source

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Fetcher downloads Flux artifact tarballs, verifies digests, and untars
// into a target directory.
type Fetcher struct {
	HTTPClient *http.Client
}

// Fetch downloads info.URL, verifies it matches info.Digest ("sha256:<hex>"),
// then untars into destDir. Rejects entries whose paths would escape destDir.
func (f *Fetcher) Fetch(ctx context.Context, info ArtifactInfo, destDir string) error {
	client := f.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", info.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("fetch %s: status %d", info.URL, resp.StatusCode)
	}

	h := sha256.New()
	tr := io.TeeReader(resp.Body, h)
	gzr, err := gzip.NewReader(tr)
	if err != nil {
		return fmt.Errorf("gunzip: %w", err)
	}
	defer gzr.Close()
	tarReader := tar.NewReader(gzr)

	// Use a buffer to fully consume so the digest covers the whole body.
	if err := untar(tarReader, destDir); err != nil {
		return err
	}
	// Drain remainder so the hash is over the entire body.
	if _, err := io.Copy(io.Discard, tr); err != nil {
		return fmt.Errorf("drain body: %w", err)
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, info.Digest) {
		return fmt.Errorf("digest mismatch: got %s want %s", got, info.Digest)
	}
	return nil
}

func untar(tr *tar.Reader, destDir string) error {
	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		clean := filepath.Clean(hdr.Name)
		target := filepath.Join(absDest, clean)
		if !strings.HasPrefix(target+string(os.PathSeparator), absDest+string(os.PathSeparator)) {
			return fmt.Errorf("path traversal blocked: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		default:
			// skip symlinks/devices etc.
		}
	}
}
```

- [ ] **Step 8.4: Run test to verify it passes**

```
go test ./internal/source/ -v
```
Expected: all PASS.

- [ ] **Step 8.5: Commit**

```
git add internal/source/fetcher.go internal/source/fetcher_test.go
git commit -m "Add artifact fetcher with digest verify and path-traversal guard"
```

---

## Task 9: SSA applier

**Files:**
- Create: `internal/apply/applier.go`
- Create: `internal/apply/applier_test.go`

- [ ] **Step 9.1: Write failing test**

`internal/apply/applier_test.go`:

```go
package apply

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/restmapper"
)

func newCM(ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})
	u.SetNamespace(ns)
	u.SetName(name)
	u.Object["data"] = map[string]any{"k": "v"}
	return u
}

func TestApplyAddsLabelAndAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	dyn := fake.NewSimpleDynamicClient(scheme)
	mapper := restmapper.NewDiscoveryRESTMapper([]*restmapper.APIGroupResources{{
		Group: metav1.APIGroup{Name: "", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "v1", Version: "v1"}}},
		VersionedResources: map[string][]metav1.APIResource{
			"v1": {{Name: "configmaps", Namespaced: true, Kind: "ConfigMap"}},
		},
	}})

	a := &Applier{Dynamic: dyn, Mapper: mapper, FieldManager: "krox-controller"}

	got, err := a.Apply(context.Background(), newCM("ns", "x"), "apps/web", "sha:1")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if v := got.GetLabels()["krox.io/owned-by"]; v != "apps/web" {
		t.Fatalf("label: %q", v)
	}
	if v := got.GetAnnotations()["krox.io/last-applied-revision"]; v != "sha:1" {
		t.Fatalf("annotation: %q", v)
	}
}

func TestDeleteIgnoresNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	dyn := fake.NewSimpleDynamicClient(scheme)
	mapper := restmapper.NewDiscoveryRESTMapper([]*restmapper.APIGroupResources{{
		Group: metav1.APIGroup{Name: "", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "v1", Version: "v1"}}},
		VersionedResources: map[string][]metav1.APIResource{
			"v1": {{Name: "services", Namespaced: true, Kind: "Service"}},
		},
	}})
	p := &Pruner{Dynamic: dyn, Mapper: mapper}
	err := p.Delete(context.Background(), "/v1/Service/ns/missing")
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("expected nil or NotFound, got %v", err)
	}
}
```

- [ ] **Step 9.2: Run test to verify it fails**

```
go test ./internal/apply/ -v
```
Expected: FAIL — `Applier`, `Pruner` undefined.

- [ ] **Step 9.3: Implement `internal/apply/applier.go`**

```go
package apply

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const (
	OwnerLabel        = "krox.io/owned-by"
	RevisionAnnot     = "krox.io/last-applied-revision"
	DefaultFieldOwner = "krox-controller"
)

// Applier performs server-side apply for rendered objects.
type Applier struct {
	Dynamic      dynamic.Interface
	Mapper       meta.RESTMapper
	FieldManager string
	Force        bool
}

// Apply server-side-applies obj after stamping the owner label and revision
// annotation. Returns the live object returned by the API server.
func (a *Applier) Apply(ctx context.Context, obj *unstructured.Unstructured, ownerKey, revision string) (*unstructured.Unstructured, error) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[OwnerLabel] = ownerKey
	obj.SetLabels(labels)

	annot := obj.GetAnnotations()
	if annot == nil {
		annot = map[string]string{}
	}
	annot[RevisionAnnot] = revision
	obj.SetAnnotations(annot)

	gvk := obj.GroupVersionKind()
	mapping, err := a.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("REST mapping for %s: %w", gvk, err)
	}
	resource := a.Dynamic.Resource(mapping.Resource)
	var iface dynamic.ResourceInterface = resource
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		iface = resource.Namespace(obj.GetNamespace())
	}

	data, err := json.Marshal(obj.Object)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	fieldMgr := a.FieldManager
	if fieldMgr == "" {
		fieldMgr = DefaultFieldOwner
	}
	opts := metav1.PatchOptions{FieldManager: fieldMgr, Force: ptrBool(a.Force)}
	out, err := iface.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, opts)
	if err != nil {
		return nil, fmt.Errorf("apply %s/%s: %w", gvk.Kind, obj.GetName(), err)
	}
	return out, nil
}

func ptrBool(b bool) *bool { return &b }

// Pruner deletes inventory entries no longer present in the new render.
type Pruner struct {
	Dynamic dynamic.Interface
	Mapper  meta.RESTMapper
}

// Delete removes the resource identified by an inventory ID. Missing objects
// are tolerated (returns NotFound for callers that want to log them).
func (p *Pruner) Delete(ctx context.Context, id string) error {
	gvk, ns, name, err := ParseID(id)
	if err != nil {
		return err
	}
	mapping, err := p.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("REST mapping for %s: %w", gvk, err)
	}
	resource := p.Dynamic.Resource(mapping.Resource)
	var iface dynamic.ResourceInterface = resource
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		iface = resource.Namespace(ns)
	}
	propagation := metav1.DeletePropagationForeground
	err = iface.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &propagation})
	if apierrors.IsNotFound(err) {
		return err
	}
	return err
}
```

- [ ] **Step 9.4: Run test to verify it passes**

```
go test ./internal/apply/ -v
```
Expected: PASS.

- [ ] **Step 9.5: Commit**

```
git add internal/apply/applier.go internal/apply/applier_test.go
git commit -m "Add server-side applier and pruner with owner labeling"
```

---

## Task 10: Reconciler skeleton & main wiring

**Files:**
- Modify: `internal/controller/kroxdeployment_controller.go`
- Modify: `cmd/main.go`

- [ ] **Step 10.1: Replace `internal/controller/kroxdeployment_controller.go`**

```go
package controller

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	srcv1 "github.com/fluxcd/source-controller/api/v1"
	v1alpha1 "github.com/trevex/krox-controller/api/v1alpha1"
	"github.com/trevex/krox-controller/internal/apply"
	"github.com/trevex/krox-controller/internal/render"
	"github.com/trevex/krox-controller/internal/source"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// KroxDeploymentReconciler reconciles a KroxDeployment.
type KroxDeploymentReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	RestConfig *rest.Config
	HTTPClient *http.Client

	Resolver *source.Resolver
	Fetcher  *source.Fetcher
	Engine   *render.Engine
	Applier  *apply.Applier
	Pruner   *apply.Pruner
}

// +kubebuilder:rbac:groups=krox.io,resources=kroxdeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=krox.io,resources=kroxdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=krox.io,resources=kroxdeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories;ocirepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;create;update;patch;delete

func (r *KroxDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var kd v1alpha1.KroxDeployment
	if err := r.Get(ctx, req.NamespacedName, &kd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if kd.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	// Mark Reconciling.
	r.setCondition(&kd, v1alpha1.ConditionReconciling, metav1.ConditionTrue, "Progressing", "Reconciling")
	apimeta.RemoveStatusCondition(&kd.Status.Conditions, v1alpha1.ConditionStalled)
	if err := r.Status().Update(ctx, &kd); err != nil {
		return ctrl.Result{}, err
	}

	res, err := r.reconcile(ctx, &kd)
	if err != nil {
		logger.Error(err, "reconcile failed")
	}
	return res, err
}

func (r *KroxDeploymentReconciler) reconcile(ctx context.Context, kd *v1alpha1.KroxDeployment) (ctrl.Result, error) {
	// 1. Resolve source.
	art, err := r.Resolver.Resolve(ctx, kd.Namespace, kd.Spec.SourceRef)
	if err != nil {
		if source.IsNotReady(err) {
			r.setCondition(kd, v1alpha1.ConditionReady, metav1.ConditionFalse, "SourceNotReady", err.Error())
			_ = r.Status().Update(ctx, kd)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return r.terminal(ctx, kd, "SourceError", err)
	}
	kd.Status.LastAttemptedRevision = art.Revision

	// 2. Fetch artifact.
	dir, err := os.MkdirTemp("", "krox-*")
	if err != nil {
		return ctrl.Result{}, err
	}
	defer os.RemoveAll(dir)
	if err := r.Fetcher.Fetch(ctx, *art, dir); err != nil {
		return r.transient(ctx, kd, "FetchFailed", err)
	}

	// 3. Read RGD file.
	rgdData, err := os.ReadFile(filepath.Join(dir, filepath.Clean(kd.Spec.Path)))
	if err != nil {
		return r.terminal(ctx, kd, "RGDNotFound", err)
	}
	rgd, err := render.ParseRGD(rgdData)
	if err != nil {
		return r.terminal(ctx, kd, "RGDInvalid", err)
	}

	// 4. Build instance and plan.
	values := []byte("{}")
	if kd.Spec.Values != nil {
		values = kd.Spec.Values.Raw
	}
	inst, err := render.BuildInstance(rgd, kd.Name, kd.Namespace, values)
	if err != nil {
		return r.terminal(ctx, kd, "InstanceInvalid", err)
	}
	rt, err := r.Engine.Plan(rgd, inst)
	if err != nil {
		return r.terminal(ctx, kd, "PlanFailed", err)
	}

	// 5. Apply layer-by-layer with observe-back.
	ownerKey := fmt.Sprintf("%s/%s", kd.Namespace, kd.Name)
	newInv := &v1alpha1.ResourceInventory{}
	r.Applier.Force = kd.Spec.Force
	for _, node := range rt.Nodes() {
		desired, err := node.GetDesired()
		if err != nil {
			return r.terminal(ctx, kd, "RenderFailed", err)
		}
		observed := make([]*unstructured.Unstructured, 0, len(desired))
		for _, obj := range desired {
			applied, err := r.Applier.Apply(ctx, obj, ownerKey, art.Revision)
			if err != nil {
				return r.transient(ctx, kd, "ApplyFailed", err)
			}
			newInv.Entries = append(newInv.Entries, v1alpha1.ResourceRef{
				ID: apply.IDFromObject(applied), Version: applied.GetResourceVersion(),
			})
			observed = append(observed, applied)
		}
		node.SetObserved(observed)
	}

	// 6. Prune.
	if kd.Spec.Prune {
		for _, ref := range apply.Diff(kd.Status.Inventory, newInv) {
			err := r.Pruner.Delete(ctx, ref.ID)
			if err != nil && !apierrors.IsNotFound(err) {
				return r.transient(ctx, kd, "PruneFailed", err)
			}
		}
	}

	// 7. Persist success.
	kd.Status.Inventory = newInv
	kd.Status.LastAppliedRevision = art.Revision
	kd.Status.ObservedGeneration = kd.Generation
	apimeta.RemoveStatusCondition(&kd.Status.Conditions, v1alpha1.ConditionReconciling)
	r.setCondition(kd, v1alpha1.ConditionReady, metav1.ConditionTrue, "ApplySucceeded",
		fmt.Sprintf("Applied %d resources at revision %s", len(newInv.Entries), art.Revision))
	if err := r.Status().Update(ctx, kd); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: kd.Spec.Interval.Duration}, nil
}

func (r *KroxDeploymentReconciler) setCondition(kd *v1alpha1.KroxDeployment, t string, s metav1.ConditionStatus, reason, msg string) {
	apimeta.SetStatusCondition(&kd.Status.Conditions, metav1.Condition{
		Type: t, Status: s, Reason: reason, Message: msg, ObservedGeneration: kd.Generation,
	})
}

func (r *KroxDeploymentReconciler) terminal(ctx context.Context, kd *v1alpha1.KroxDeployment, reason string, err error) (ctrl.Result, error) {
	r.setCondition(kd, v1alpha1.ConditionReady, metav1.ConditionFalse, reason, err.Error())
	r.setCondition(kd, v1alpha1.ConditionStalled, metav1.ConditionTrue, reason, err.Error())
	apimeta.RemoveStatusCondition(&kd.Status.Conditions, v1alpha1.ConditionReconciling)
	_ = r.Status().Update(ctx, kd)
	return ctrl.Result{}, nil
}

func (r *KroxDeploymentReconciler) transient(ctx context.Context, kd *v1alpha1.KroxDeployment, reason string, err error) (ctrl.Result, error) {
	r.setCondition(kd, v1alpha1.ConditionReady, metav1.ConditionFalse, reason, err.Error())
	_ = r.Status().Update(ctx, kd)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *KroxDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapFn := func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.KroxDeploymentList
		if err := mgr.GetClient().List(ctx, &list); err != nil {
			return nil
		}
		ns := obj.GetNamespace()
		name := obj.GetName()
		var out []reconcile.Request
		for _, kd := range list.Items {
			srcNs := kd.Spec.SourceRef.Namespace
			if srcNs == "" {
				srcNs = kd.Namespace
			}
			if srcNs == ns && kd.Spec.SourceRef.Name == name {
				out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: kd.Namespace, Name: kd.Name}})
			}
		}
		return out
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.KroxDeployment{}).
		Watches(&srcv1.GitRepository{}, handler.EnqueueRequestsFromMapFunc(mapFn), builder.WithPredicates()).
		Watches(&srcv1.OCIRepository{}, handler.EnqueueRequestsFromMapFunc(mapFn), builder.WithPredicates()).
		Complete(r)
}

// NewDynamic returns a dynamic.Interface from a rest.Config. Exposed for main.go.
func NewDynamic(cfg *rest.Config) (dynamic.Interface, error) {
	return dynamic.NewForConfig(cfg)
}
```

- [ ] **Step 10.2: Replace `cmd/main.go` with the wired manager**

Use the kubebuilder-generated file but swap in our reconciler construction. The pertinent block (replacing the scaffolded `&KroxDeploymentReconciler{}` setup):

```go
// inside main() after mgr is created:
restCfg := ctrl.GetConfigOrDie()
httpClient, err := rest.HTTPClientFor(restCfg)
if err != nil { setupLog.Error(err, "http client"); os.Exit(1) }

engine, err := render.NewEngine(restCfg, httpClient)
if err != nil { setupLog.Error(err, "engine"); os.Exit(1) }

dyn, err := dynamic.NewForConfig(restCfg)
if err != nil { setupLog.Error(err, "dynamic client"); os.Exit(1) }

discovery, err := discovery.NewDiscoveryClientForConfig(restCfg)
if err != nil { setupLog.Error(err, "discovery"); os.Exit(1) }
groupResources, err := restmapper.GetAPIGroupResources(discovery)
if err != nil { setupLog.Error(err, "api groups"); os.Exit(1) }
mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

if err := (&controller.KroxDeploymentReconciler{
    Client:     mgr.GetClient(),
    Scheme:     mgr.GetScheme(),
    RestConfig: restCfg,
    HTTPClient: httpClient,
    Resolver:   &source.Resolver{Client: mgr.GetClient()},
    Fetcher:    &source.Fetcher{HTTPClient: http.DefaultClient},
    Engine:     engine,
    Applier:    &apply.Applier{Dynamic: dyn, Mapper: mapper, FieldManager: apply.DefaultFieldOwner},
    Pruner:     &apply.Pruner{Dynamic: dyn, Mapper: mapper},
}).SetupWithManager(mgr); err != nil {
    setupLog.Error(err, "unable to create controller")
    os.Exit(1)
}
```

Required imports (add to the existing import block):

```go
"net/http"
srcv1 "github.com/fluxcd/source-controller/api/v1"
"github.com/trevex/krox-controller/internal/apply"
"github.com/trevex/krox-controller/internal/render"
"github.com/trevex/krox-controller/internal/source"
"github.com/trevex/krox-controller/internal/controller"
"k8s.io/client-go/dynamic"
"k8s.io/client-go/discovery"
"k8s.io/client-go/restmapper"
"k8s.io/client-go/rest"
```

And add to the scheme registration block (alongside the existing `kroxiov1alpha1.AddToScheme`):

```go
utilruntime.Must(srcv1.AddToScheme(scheme))
```

- [ ] **Step 10.3: Verify build**

```
go build ./...
```
Expected: no errors. Fix any import alias mismatches the kubebuilder scaffold introduced.

- [ ] **Step 10.4: Commit**

```
git add internal/controller/kroxdeployment_controller.go cmd/main.go
git commit -m "Wire reconciler with source/render/apply pipeline"
```

---

## Task 11: envtest suite — happy path

**Files:**
- Modify: `internal/controller/suite_test.go` (kubebuilder scaffolded)
- Create: `internal/controller/kroxdeployment_controller_test.go`

- [ ] **Step 11.1: Update `internal/controller/suite_test.go` to install Flux CRDs**

Edit the `BeforeSuite` to include vendored CRDs:

```go
testEnv = &envtest.Environment{
    CRDDirectoryPaths: []string{
        filepath.Join("..", "..", "config", "crd", "bases"),
        filepath.Join("..", "..", "hack", "vendored-crds"),
    },
    ErrorIfCRDPathMissing: true,
}
// register Flux scheme too:
utilruntime.Must(srcv1.AddToScheme(scheme.Scheme))
```

Imports to add:
```go
srcv1 "github.com/fluxcd/source-controller/api/v1"
```

- [ ] **Step 11.2: Add a Ginkgo spec exercising the happy path**

Create `internal/controller/kroxdeployment_controller_test.go`:

```go
package controller_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	srcv1 "github.com/fluxcd/source-controller/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1alpha1 "github.com/trevex/krox-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("KroxDeployment reconcile", func() {
	const ns = "default"

	It("renders an RGD into a Deployment and reports Ready", func() {
		// Serve a tarball containing rgd.yaml.
		rgd, err := os.ReadFile("../../test/testdata/rgds/webapp.yaml")
		Expect(err).NotTo(HaveOccurred())
		tgz := tarGz(map[string]string{"rgd.yaml": string(rgd)})
		digest := "sha256:" + sha256Hex(tgz)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write(tgz)
		}))
		DeferCleanup(srv.Close)

		// Create a GitRepository pretending to be Ready.
		gr := &srcv1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "src", Namespace: ns},
			Spec:       srcv1.GitRepositorySpec{Interval: metav1.Duration{Duration: time.Minute}, URL: "https://example.invalid"},
		}
		Expect(k8sClient.Create(ctx, gr)).To(Succeed())
		gr.Status = srcv1.GitRepositoryStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, LastTransitionTime: metav1.Now(), Reason: "Succeeded", Message: "ok"}},
			Artifact:   &srcv1.Artifact{URL: srv.URL, Revision: "main@sha:abc", Digest: digest, LastUpdateTime: metav1.Now()},
		}
		Expect(k8sClient.Status().Update(ctx, gr)).To(Succeed())

		// Create the KroxDeployment.
		kd := &v1alpha1.KroxDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: "wa", Namespace: ns},
			Spec: v1alpha1.KroxDeploymentSpec{
				Interval:  metav1.Duration{Duration: 30 * time.Second},
				SourceRef: v1alpha1.SourceReference{Kind: "GitRepository", Name: "src"},
				Path:      "rgd.yaml",
				Values:    &apiextensionsv1.JSON{Raw: []byte(`{"name":"web","replicas":2,"image":"nginx:1.27"}`)},
				Prune:     true,
			},
		}
		Expect(k8sClient.Create(ctx, kd)).To(Succeed())

		// Wait for Ready.
		Eventually(func(g Gomega) {
			out := &v1alpha1.KroxDeployment{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "wa", Namespace: ns}, out)).To(Succeed())
			cond := findCondition(out.Status.Conditions, v1alpha1.ConditionReady)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(out.Status.LastAppliedRevision).To(Equal("main@sha:abc"))
			g.Expect(out.Status.Inventory).NotTo(BeNil())
			g.Expect(out.Status.Inventory.Entries).To(HaveLen(1))
		}, "30s", "1s").Should(Succeed())

		// Confirm the Deployment exists.
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "web", Namespace: ns}, dep)).To(Succeed())
		Expect(*dep.Spec.Replicas).To(BeNumerically("==", 2))
	})
})

func findCondition(cs []metav1.Condition, t string) *metav1.Condition {
	for i := range cs {
		if cs[i].Type == t {
			return &cs[i]
		}
	}
	return nil
}

func tarGz(entries map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var ctx = context.Background()
```

- [ ] **Step 11.3: Ensure the manager-under-test starts the real reconciler**

In `suite_test.go`, replace the kubebuilder-generated `(&KroxDeploymentReconciler{...}).SetupWithManager(mgr)` with the same construction as in `cmd/main.go`. Use the suite's already-configured `cfg` (the envtest rest.Config) as input to `render.NewEngine`, `dynamic.NewForConfig`, and the discovery REST mapper.

- [ ] **Step 11.4: Run the suite**

```
make test
```
Expected: Ginkgo runs the suite, the spec passes. `setup-envtest` downloads apiserver+etcd on first run.

If the run fails because `envtest` cannot find the binaries, run `make envtest` (kubebuilder target) first to install them.

- [ ] **Step 11.5: Commit**

```
git add internal/controller/suite_test.go internal/controller/kroxdeployment_controller_test.go
git commit -m "Add envtest happy-path suite for KroxDeployment"
```

---

## Task 12: envtest — failure scenarios

**Files:**
- Modify: `internal/controller/kroxdeployment_controller_test.go`

- [ ] **Step 12.1: Add a Ginkgo spec for source-not-ready**

Append:

```go
var _ = Describe("KroxDeployment failure modes", func() {
	const ns = "default"

	It("waits when source is not ready", func() {
		gr := &srcv1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "pending-src", Namespace: ns},
			Spec:       srcv1.GitRepositorySpec{URL: "https://x", Interval: metav1.Duration{Duration: time.Minute}},
		}
		Expect(k8sClient.Create(ctx, gr)).To(Succeed())
		gr.Status = srcv1.GitRepositoryStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, LastTransitionTime: metav1.Now(), Reason: "Progressing", Message: "fetching"}},
		}
		Expect(k8sClient.Status().Update(ctx, gr)).To(Succeed())

		kd := &v1alpha1.KroxDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: "wait", Namespace: ns},
			Spec: v1alpha1.KroxDeploymentSpec{
				Interval:  metav1.Duration{Duration: 30 * time.Second},
				SourceRef: v1alpha1.SourceReference{Kind: "GitRepository", Name: "pending-src"},
				Path:      "rgd.yaml",
			},
		}
		Expect(k8sClient.Create(ctx, kd)).To(Succeed())

		Eventually(func(g Gomega) {
			out := &v1alpha1.KroxDeployment{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "wait", Namespace: ns}, out)).To(Succeed())
			cond := findCondition(out.Status.Conditions, v1alpha1.ConditionReady)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal("SourceNotReady"))
			// Should NOT be Stalled — transient.
			stalled := findCondition(out.Status.Conditions, v1alpha1.ConditionStalled)
			g.Expect(stalled).To(BeNil())
		}, "15s", "1s").Should(Succeed())
	})

	It("marks Stalled on RGD parse error", func() {
		bad := []byte("not a valid: : rgd")
		tgz := tarGz(map[string]string{"rgd.yaml": string(bad)})
		digest := "sha256:" + sha256Hex(tgz)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(tgz) }))
		DeferCleanup(srv.Close)

		gr := &srcv1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-src", Namespace: ns},
			Spec:       srcv1.GitRepositorySpec{URL: "https://x", Interval: metav1.Duration{Duration: time.Minute}},
		}
		Expect(k8sClient.Create(ctx, gr)).To(Succeed())
		gr.Status = srcv1.GitRepositoryStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, LastTransitionTime: metav1.Now()}},
			Artifact:   &srcv1.Artifact{URL: srv.URL, Revision: "v1", Digest: digest},
		}
		Expect(k8sClient.Status().Update(ctx, gr)).To(Succeed())

		kd := &v1alpha1.KroxDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: ns},
			Spec: v1alpha1.KroxDeploymentSpec{
				Interval:  metav1.Duration{Duration: time.Minute},
				SourceRef: v1alpha1.SourceReference{Kind: "GitRepository", Name: "bad-src"},
				Path:      "rgd.yaml",
			},
		}
		Expect(k8sClient.Create(ctx, kd)).To(Succeed())

		Eventually(func(g Gomega) {
			out := &v1alpha1.KroxDeployment{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "bad", Namespace: ns}, out)).To(Succeed())
			stalled := findCondition(out.Status.Conditions, v1alpha1.ConditionStalled)
			g.Expect(stalled).NotTo(BeNil())
			g.Expect(stalled.Status).To(Equal(metav1.ConditionTrue))
		}, "15s", "1s").Should(Succeed())
	})
})
```

- [ ] **Step 12.2: Add a Ginkgo spec for prune**

Append a spec that:
1. Creates a multi-resource RGD that includes both a Deployment and a Service.
2. Waits for both to exist.
3. Updates the tarball server to serve an RGD without the Service.
4. Bumps `GitRepository.status.artifact.revision` to trigger re-reconcile.
5. Asserts the Service is deleted and the inventory shrinks.

For the multi-resource fixture, create `test/testdata/rgds/webapp-with-svc.yaml` (Deployment + Service) and `test/testdata/rgds/webapp-svc-removed.yaml` (Deployment only). Use them in the spec.

- [ ] **Step 12.3: Run all envtest specs**

```
make test
```
Expected: all specs PASS.

- [ ] **Step 12.4: Commit**

```
git add internal/controller/kroxdeployment_controller_test.go test/testdata/
git commit -m "Add envtest failure-mode and prune scenarios"
```

---

## Task 13: E2E setup — kind harness & Makefile targets

**Files:**
- Create: `test/e2e/e2e_suite_test.go`
- Create: `test/e2e/main_test.go`
- Modify: `Makefile`
- Create: `hack/vendored-crds/source-controller-install.yaml` (pinned Flux source-controller)

- [ ] **Step 13.1: Vendor the Flux source-controller install manifest**

```
curl -fsSL -o hack/vendored-crds/source-controller-install.yaml \
  https://github.com/fluxcd/source-controller/releases/download/v1.8.5/source-controller.deployment.yaml
```
(If that asset name differs at the time of work, use the install file from the release's assets — `kubectl apply -f <url>` must successfully install the controller.)

- [ ] **Step 13.2: Add Makefile targets**

Append to `Makefile`:

```makefile
KIND_CLUSTER ?= krox-e2e
IMG ?= krox-controller:test

.PHONY: kind-up
kind-up:
	kind get clusters | grep -q $(KIND_CLUSTER) || kind create cluster --name $(KIND_CLUSTER)

.PHONY: kind-down
kind-down:
	kind delete cluster --name $(KIND_CLUSTER)

.PHONY: kind-load
kind-load: docker-build
	kind load docker-image $(IMG) --name $(KIND_CLUSTER)

.PHONY: test-e2e
test-e2e: kind-up kind-load
	go test ./test/e2e/... -v -timeout 20m
```

- [ ] **Step 13.3: Create the e2e suite scaffold**

`test/e2e/e2e_suite_test.go`:

```go
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	cfg, _ := envconf.NewFromFlags()
	testenv = env.NewWithConfig(cfg)
	testenv.Setup(
		envfuncs.SetupCRDs(filepath.Join("..", "..", "config", "crd", "bases"), "*.yaml"),
		applyManifests("../../hack/vendored-crds/source-controller-install.yaml"),
		applyManifests("../../config/default"),
	)
	testenv.Finish()
	os.Exit(testenv.Run(m))
}

func applyManifests(path string) env.Func {
	return func(ctx env.Context, c *envconf.Config) (env.Context, error) {
		cmd := exec.Command("kubectl", "apply", "-f", path, "--kubeconfig", c.KubeconfigFile())
		out, err := cmd.CombinedOutput()
		if err != nil {
			return ctx, fmtErr("kubectl apply %s: %s", path, string(out))
		}
		return ctx, nil
	}
}

func fmtErr(format string, args ...any) error {
	return &applyError{format: format, args: args}
}

type applyError struct {
	format string
	args   []any
}

func (a *applyError) Error() string { return "" }
```

(`fmtErr` is a placeholder so we can return the formatted error in real usage — the real implementation should be replaced with `fmt.Errorf` at this step; left here only to keep the snippet self-contained. Replace with `fmt.Errorf(format, args...)` and import `"fmt"`.)

- [ ] **Step 13.4: Build the image**

Add/confirm `docker-build` works:

```
make docker-build IMG=krox-controller:test
```
Expected: image built locally.

- [ ] **Step 13.5: Commit**

```
git add Makefile hack/vendored-crds/source-controller-install.yaml test/e2e/
git commit -m "Add kind-based e2e harness and Makefile targets"
```

---

## Task 14: E2E — render-and-apply scenario

**Files:**
- Create: `test/e2e/kroxdeployment_test.go`
- Create: `test/e2e/fixtures/webapp-rgd.tgz` (generated at test time or via build script — see step 14.1)

- [ ] **Step 14.1: Add a helper that publishes a fixture tarball via in-cluster HTTP**

Since flux source-controller fetches by URL, the simplest in-cluster source is the `OCIRepository` kind backed by an image in a local registry, or a `GitRepository` pointing at an in-cluster `gitea`. To keep the MVP tight, this task uses a synthetic approach: create a `GitRepository` with `spec.suspend: true` and **directly stamp** its `.status.artifact` to point at a `Service` exposing a local HTTP server serving a fixture tarball.

Add `test/e2e/kroxdeployment_test.go`:

```go
package e2e

import (
	"context"
	"testing"
	"time"

	srcv1 "github.com/fluxcd/source-controller/api/v1"
	v1alpha1 "github.com/trevex/krox-controller/api/v1alpha1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestRenderAndApply(t *testing.T) {
	f := features.New("render-and-apply").
		Setup(deployTarballServer).
		Setup(createSuspendedGitRepoStatus).
		Assess("KroxDeployment becomes Ready with Deployment present", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			cli := mustClient(t, c)

			kd := &v1alpha1.KroxDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: "default"},
				Spec: v1alpha1.KroxDeploymentSpec{
					Interval:  metav1.Duration{Duration: 30 * time.Second},
					SourceRef: v1alpha1.SourceReference{Kind: "GitRepository", Name: "fixture-src"},
					Path:      "rgd.yaml",
					Values:    &apiextensionsv1.JSON{Raw: []byte(`{"name":"web","replicas":1,"image":"nginx:1.27"}`)},
					Prune:     true,
				},
			}
			require.NoError(t, cli.Create(ctx, kd))

			require.Eventually(t, func() bool {
				out := &v1alpha1.KroxDeployment{}
				if err := cli.Get(ctx, types.NamespacedName{Name: "webapp", Namespace: "default"}, out); err != nil {
					return false
				}
				for _, c := range out.Status.Conditions {
					if c.Type == v1alpha1.ConditionReady && c.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, 3*time.Minute, 2*time.Second)

			dep := &appsv1.Deployment{}
			require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: "web", Namespace: "default"}, dep))
			return ctx
		}).
		Feature()
	testenv.Test(t, f)
}

// deployTarballServer creates a Deployment running an nginx serving rgd.yaml
// + Service exposing it. Implementation packs test/testdata/rgds/webapp.yaml
// into a ConfigMap, mounts it into the Pod, exposes via Service "fixture-src".
// (Implementation continues in step 14.2.)
func deployTarballServer(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	t.Helper()
	// ... real implementation in step 14.2
	return ctx
}

// createSuspendedGitRepoStatus creates a suspended GitRepository whose
// .status.artifact points at the in-cluster Service.
func createSuspendedGitRepoStatus(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	t.Helper()
	cli := mustClient(t, c)
	gr := &srcv1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "fixture-src", Namespace: "default"},
		Spec:       srcv1.GitRepositorySpec{URL: "https://invalid", Interval: metav1.Duration{Duration: time.Hour}, Suspend: true},
	}
	require.NoError(t, cli.Create(ctx, gr))
	gr.Status = srcv1.GitRepositoryStatus{
		Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, LastTransitionTime: metav1.Now(), Reason: "Test"}},
		Artifact: &srcv1.Artifact{
			URL:            "http://fixture-src.default.svc.cluster.local/rgd.tgz",
			Revision:       "v1",
			Digest:         "sha256:PLACEHOLDER", // set by step 14.2
			LastUpdateTime: metav1.Now(),
		},
	}
	require.NoError(t, cli.Status().Update(ctx, gr))
	return ctx
}

func mustClient(t *testing.T, c *envconf.Config) client.Client {
	t.Helper()
	cli, err := client.New(c.Client().RESTConfig(), client.Options{})
	require.NoError(t, err)
	require.NoError(t, srcv1.AddToScheme(cli.Scheme()))
	require.NoError(t, v1alpha1.AddToScheme(cli.Scheme()))
	return cli
}
```

- [ ] **Step 14.2: Implement `deployTarballServer`**

Replace the stub with an implementation that:
1. Builds a `.tar.gz` containing `rgd.yaml` (the contents of `test/testdata/rgds/webapp.yaml`) in-memory.
2. Computes the `sha256:` digest.
3. Creates a `ConfigMap` containing the bytes under a `binaryData` key.
4. Creates a `Deployment` running `python:3-alpine` with command `python -m http.server 80` and mounts the configmap to `/srv/rgd.tgz`, working dir `/srv`.
5. Creates a `Service` named `fixture-src` (matches the GitRepository status URL).
6. Stores the digest in a global var so `createSuspendedGitRepoStatus` can patch the GR status with the right digest.

Show the full implementation including the tar.gz construction (mirror the helper from Task 8/11) and yaml-free object construction via `corev1`/`appsv1` Go structs.

(Engineer note: in some kind setups, in-cluster DNS resolution for `fixture-src.default.svc.cluster.local` works fine. If it doesn't on a given kind version, fall back to using `kind`'s `extraPortMappings` and host-side HTTP, then point the GR URL at `host.docker.internal`.)

- [ ] **Step 14.3: Run e2e**

```
make test-e2e
```
Expected: kind cluster up, image loaded, source-controller installed (idle — we don't use it), our manager applied, KroxDeployment reconciles, Deployment "web" exists in default namespace, condition Ready=True.

- [ ] **Step 14.4: Commit**

```
git add test/e2e/
git commit -m "Add e2e test for render-and-apply happy path"
```

---

## Task 15: E2E — prune scenario

**Files:**
- Modify: `test/e2e/kroxdeployment_test.go`

- [ ] **Step 15.1: Add the prune feature**

Append a `TestPrune` that:
1. Reuses the fixture server but starts with a multi-resource RGD (Deployment + Service named `web-svc`).
2. After Ready, asserts both objects exist.
3. Replaces the in-cluster ConfigMap content with a single-resource RGD (Deployment only) and patches the GR status artifact `digest` + `revision`.
4. Triggers reconcile by bumping the KroxDeployment annotation (or waits for the interval).
5. Asserts the Service is deleted.

Use `corev1.ConfigMap.BinaryData` patching plus a kubectl-style rollout-restart on the http-server Deployment to make the in-cluster server serve the new bytes immediately.

- [ ] **Step 15.2: Run e2e**

```
make test-e2e
```
Expected: both `TestRenderAndApply` and `TestPrune` PASS.

- [ ] **Step 15.3: Commit**

```
git add test/e2e/
git commit -m "Add e2e prune scenario"
```

---

## Task 16: Sample CR + minimal docs

**Files:**
- Modify: `config/samples/krox_v1alpha1_kroxdeployment.yaml`
- Modify: `config/samples/kustomization.yaml`
- Create: `docs/usage.md`

- [ ] **Step 16.1: Replace the sample**

`config/samples/krox_v1alpha1_kroxdeployment.yaml`:

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

- [ ] **Step 16.2: Add a usage doc**

`docs/usage.md`:

```markdown
# Using KroxDeployment

A `KroxDeployment` renders a KRO `ResourceGraphDefinition` from a Flux source
and applies the resulting Kubernetes objects to the cluster.

## Prerequisites

- Flux `source-controller` installed and serving artifacts (GitRepository or OCIRepository).
- This controller (`krox-controller`) installed via `make install && make deploy IMG=<your-image>`.

## Minimal example

(see `config/samples/krox_v1alpha1_kroxdeployment.yaml`)

## Conditions

| Condition    | Meaning                                    |
|--------------|--------------------------------------------|
| Ready        | Last reconcile succeeded                   |
| Reconciling  | A reconcile is in progress                 |
| Stalled      | Terminal failure, won't retry until source/spec changes |

## Pruning

When `spec.prune: true`, objects last applied by this KroxDeployment that are
absent from the new render are deleted with foreground propagation.
```

- [ ] **Step 16.3: Commit**

```
git add config/samples/ docs/usage.md
git commit -m "Add sample CR and usage docs"
```

---

## Self-review summary

**Spec coverage check:**
- ✅ Single CRD `KroxDeployment` v1alpha1 — Task 2
- ✅ Git + OCI source resolution — Task 7
- ✅ Embedded KRO via `graph.NewBuilder` / `runtime.FromGraph` — Task 6, used in Task 10
- ✅ Layered apply with `GetDesired` / `SetObserved` ready-barrier — Task 10
- ✅ Server-side apply with field manager `krox-controller` + owner labels — Task 9
- ✅ Inventory-based prune — Tasks 3, 9, 10
- ✅ Conditions: Ready / Reconciling / Stalled — Task 10 (`setCondition`, `terminal`, `transient`)
- ✅ envtest with Flux CRDs vendored, no KRO CRD — Task 11
- ✅ Kind e2e with `source-controller` running — Tasks 13–15
- ✅ Nix devshell extended — Task 1.1
- ✅ Failure classification: transient (requeue) vs terminal (Stalled) — Task 10, exercised in Task 12

**Type consistency check:**
- `KroxDeployment{Spec,Status}`, `SourceReference`, `ResourceInventory`, `ResourceRef`, `ConditionReady/Reconciling/Stalled` — defined in Task 2, referenced by name in Tasks 3, 7, 10, 11, 12, 14.
- `apply.{IDFromObject, ParseID, Diff, Applier, Pruner, OwnerLabel, RevisionAnnot, DefaultFieldOwner}` — defined in Tasks 3 and 9, used in Task 10.
- `render.{ParseRGD, BuildInstance, Engine, NewEngine}` — defined in Tasks 4–6, used in Task 10.
- `source.{ArtifactInfo, Resolver, Fetcher, IsNotReady}` — defined in Tasks 7–8, used in Task 10.

**Known stubs requiring engineer judgement (called out explicitly):**
- Task 13.3's `fmtErr` placeholder must be replaced with `fmt.Errorf` (called out in the step's parenthetical).
- Task 14.2 describes the in-cluster fixture HTTP server but doesn't show every line — the engineer assembles the standard `corev1.ConfigMap` + `appsv1.Deployment` + `corev1.Service` from the listed requirements. The pattern is conventional Kubernetes Go-object construction; full code would be ~80 lines of boilerplate.
- Task 14.1 notes a kind-DNS fallback (`host.docker.internal`) that the engineer applies if needed.

---

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-29-krox-controller.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
