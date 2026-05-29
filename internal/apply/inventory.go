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
