package apply

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const (
	// OwnerAnnotation records the parent KroxDeployment as "<namespace>/<name>".
	// Stored as an annotation (not a label) because the "/" separator is not
	// permitted in label values.
	OwnerAnnotation   = "krox.io/owned-by"
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
	annot := obj.GetAnnotations()
	if annot == nil {
		annot = map[string]string{}
	}
	annot[OwnerAnnotation] = ownerKey
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

// Delete removes the resource identified by an inventory ID. NotFound errors
// are returned to the caller (not swallowed) so the controller can log them.
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
	return iface.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &propagation})
}
