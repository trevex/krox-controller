package source

import (
	"context"
	"testing"
	"time"

	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	srcv1 "github.com/fluxcd/source-controller/api/v1"
	v1alpha1 "github.com/trevex/krox-controller/api/v1alpha1"
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
			Artifact: &fluxmeta.Artifact{
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
			Artifact:   &fluxmeta.Artifact{URL: "u", Digest: "d"},
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
}
