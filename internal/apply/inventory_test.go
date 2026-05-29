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
