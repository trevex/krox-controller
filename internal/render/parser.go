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
	if rgd.Spec.Schema == nil || rgd.Spec.Schema.Kind == "" {
		return nil, fmt.Errorf("RGD %q missing spec.schema.kind", rgd.Name)
	}
	if len(rgd.Spec.Resources) == 0 {
		return nil, fmt.Errorf("RGD %q has no resources", rgd.Name)
	}
	return rgd, nil
}
