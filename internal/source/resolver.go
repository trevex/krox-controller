// Package source provides helpers for resolving Flux source CR artifacts
// referenced by a KroxDeployment.
package source

import (
	"context"
	"errors"
	"fmt"

	fluxmeta "github.com/fluxcd/pkg/apis/meta"
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

func artifactFromConditions(conds []metav1.Condition, art *fluxmeta.Artifact, key types.NamespacedName) (*ArtifactInfo, error) {
	ready := apimeta.FindStatusCondition(conds, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue {
		return nil, &notReadyError{msg: fmt.Sprintf("source %s not Ready", key)}
	}
	if art == nil || art.URL == "" {
		return nil, &notReadyError{msg: fmt.Sprintf("source %s has no artifact", key)}
	}
	return &ArtifactInfo{URL: art.URL, Revision: art.Revision, Digest: art.Digest}, nil
}
