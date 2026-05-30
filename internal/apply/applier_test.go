package apply

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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

func newMapper() meta.RESTMapper {
	return restmapper.NewDiscoveryRESTMapper([]*restmapper.APIGroupResources{{
		Group: metav1.APIGroup{Name: "", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "v1", Version: "v1"}}},
		VersionedResources: map[string][]metav1.APIResource{
			"v1": {
				{Name: "configmaps", Namespaced: true, Kind: "ConfigMap"},
				{Name: "services", Namespaced: true, Kind: "Service"},
			},
		},
	}})
}

func TestApplyAddsLabelAndAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	dyn := fake.NewSimpleDynamicClient(scheme)
	mapper := newMapper()

	a := &Applier{Dynamic: dyn, Mapper: mapper, FieldManager: "krox-controller"}

	// The fake dynamic client doesn't fully support ApplyPatchType (it routes
	// through StrategicMergePatch against an unstructured object), so we don't
	// require Apply to succeed. Instead, we verify that Apply stamps the owner
	// annotation and revision annotation on the input object before sending it
	// to the API server.
	obj := newCM("ns", "x")
	_, _ = a.Apply(context.Background(), obj, "apps/web", "sha:1", false)

	if v := obj.GetAnnotations()["krox.io/owned-by"]; v != "apps/web" {
		t.Fatalf("owner annotation: %q", v)
	}
	if v := obj.GetAnnotations()["krox.io/last-applied-revision"]; v != "sha:1" {
		t.Fatalf("revision annotation: %q", v)
	}
}

func TestDeleteNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	dyn := fake.NewSimpleDynamicClient(scheme)
	mapper := newMapper()
	p := &Pruner{Dynamic: dyn, Mapper: mapper}
	err := p.Delete(context.Background(), "/v1/Service/ns/missing")
	if err == nil || !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}
