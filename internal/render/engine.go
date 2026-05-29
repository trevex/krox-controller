package render

import (
	"fmt"
	"net/http"

	krov1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"github.com/kubernetes-sigs/kro/pkg/graph"
	kroruntime "github.com/kubernetes-sigs/kro/pkg/runtime"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

// Engine builds KRO runtime instances. It is safe for concurrent use; the
// underlying graph.Builder caches OpenAPI schemas keyed by GVK.
type Engine struct {
	builder *graph.Builder
}

// NewEngine constructs an Engine backed by the given rest.Config + http.Client.
// httpClient may be nil; the Builder will create a default client.
func NewEngine(cfg *rest.Config, httpClient *http.Client) (*Engine, error) {
	b, err := graph.NewBuilder(cfg, httpClient)
	if err != nil {
		return nil, fmt.Errorf("new graph builder: %w", err)
	}
	return &Engine{builder: b}, nil
}

// Plan parses+validates the RGD and returns a runtime ready to render against
// the instance. Errors here are terminal (schema or CEL validation failures).
func (e *Engine) Plan(rgd *krov1.ResourceGraphDefinition, instance *unstructured.Unstructured) (*kroruntime.Runtime, error) {
	g, err := e.builder.NewResourceGraphDefinition(rgd, graph.RGDConfig{})
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}
	rt, err := kroruntime.FromGraph(g, instance, graph.RGDConfig{})
	if err != nil {
		return nil, fmt.Errorf("init runtime: %w", err)
	}
	return rt, nil
}
