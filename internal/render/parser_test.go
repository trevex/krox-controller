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
