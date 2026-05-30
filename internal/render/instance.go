package render

import (
	"fmt"

	krov1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utiljson "k8s.io/apimachinery/pkg/util/json"
)

// BuildInstance builds an *unstructured.Unstructured of the RGD's synthesized
// GVK with the provided values placed under spec. The instance kind is taken
// from the RGD's schema; the apiVersion is forced to "kro.run/v1alpha1" so
// downstream KRO machinery can match its schema cache.
//
// Values are decoded using apimachinery's int-preserving JSON unmarshaller so
// whole-number JSON values become int64 (not float64). KRO's CEL evaluator
// strictly requires int64 for "integer"-typed schema fields.
func BuildInstance(rgd *krov1.ResourceGraphDefinition, name, namespace string, values []byte) (*unstructured.Unstructured, error) {
	spec := map[string]any{}
	if len(values) > 0 {
		if err := utiljson.Unmarshal(values, &spec); err != nil {
			return nil, fmt.Errorf("decode values: %w", err)
		}
	}
	if rgd.Spec.Schema == nil {
		return nil, fmt.Errorf("RGD %q has no schema", rgd.Name)
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
