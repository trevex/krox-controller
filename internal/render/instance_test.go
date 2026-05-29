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
	if gvk.Kind != "WebApp" {
		t.Fatalf("kind %q", gvk.Kind)
	}
	if inst.GetName() != "myapp" {
		t.Fatalf("name %q", inst.GetName())
	}
	if inst.GetNamespace() != "production" {
		t.Fatalf("ns %q", inst.GetNamespace())
	}
	spec, ok := inst.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("spec missing or wrong type: %T", inst.Object["spec"])
	}
	if spec["name"] != "web" || spec["image"] != "nginx:1.27" {
		t.Fatalf("spec: %+v", spec)
	}

	// nil values produces empty spec.
	inst2, err := BuildInstance(rgd, "x", "ns", nil)
	if err != nil {
		t.Fatalf("nil values: %v", err)
	}
	s, ok := inst2.Object["spec"].(map[string]any)
	if !ok || len(s) != 0 {
		t.Fatalf("expected empty spec, got %+v ok=%v", s, ok)
	}
}
