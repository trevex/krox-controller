/*
Copyright 2026 krox-controller authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package e2e is the kind-based end-to-end test suite for krox-controller.
//
// The suite expects a running kind cluster with the krox-controller image
// pre-loaded. Use `make test-e2e IMG=krox-controller:test` to drive the
// full cycle (kind-up, image build+load, then `go test`).
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// testenv is the package-wide e2e-framework environment shared by all specs.
// It is initialised in TestMain after the cluster has been seeded with the
// Flux source-controller and the krox-controller manager.
var testenv env.Environment

// krox-controller manager Deployment coordinates (matches `config/default`
// kustomize output). Specs and per-feature setups may reuse these.
const (
	kroxControllerNamespace      = "krox-controller-system"
	kroxControllerDeploymentName = "krox-controller-controller-manager"

	fluxSystemNamespace            = "flux-system"
	sourceControllerDeploymentName = "source-controller"
)

func TestMain(m *testing.M) {
	cfg, err := envconf.NewFromFlags()
	if err != nil {
		panic(fmt.Errorf("envconf: %w", err))
	}
	testenv = env.NewWithConfig(cfg)

	testenv.Setup(
		applyManifest("../../hack/vendored-crds/source-controller-install.yaml", fluxSystemNamespace),
		applyKustomize("../../config/default"),
		waitForDeployment(sourceControllerDeploymentName, fluxSystemNamespace, 3*time.Minute),
		waitForDeployment(kroxControllerDeploymentName, kroxControllerNamespace, 3*time.Minute),
	)

	os.Exit(testenv.Run(m))
}

// applyManifest runs `kubectl apply -f path [-n namespace]` against the env's
// kubeconfig. Resources in the manifest with an explicit `metadata.namespace`
// keep that value; the -n flag only applies to bare resources.
func applyManifest(path, namespace string) env.Func {
	return func(ctx context.Context, c *envconf.Config) (context.Context, error) {
		args := []string{"apply", "-f", path, "--kubeconfig", c.KubeconfigFile()}
		if namespace != "" {
			args = append(args, "--namespace", namespace)
		}
		out, err := exec.Command("kubectl", args...).CombinedOutput()
		if err != nil {
			return ctx, fmt.Errorf("kubectl apply %s: %w\n%s", path, err, string(out))
		}
		return ctx, nil
	}
}

// applyKustomize builds the kustomization at dir and pipes the result to
// `kubectl apply -f -`. This avoids requiring the caller to materialise the
// rendered manifest on disk.
func applyKustomize(dir string) env.Func {
	return func(ctx context.Context, c *envconf.Config) (context.Context, error) {
		built, err := exec.Command("kustomize", "build", dir).Output()
		if err != nil {
			return ctx, fmt.Errorf("kustomize build %s: %w", dir, err)
		}
		cmd := exec.Command("kubectl", "apply", "-f", "-", "--kubeconfig", c.KubeconfigFile())
		cmd.Stdin = bytes.NewReader(built)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return ctx, fmt.Errorf("kubectl apply (kustomize %s): %w\n%s", dir, err, string(out))
		}
		return ctx, nil
	}
}

// waitForDeployment polls until the named Deployment in the given namespace
// reports `.status.availableReplicas > 0` or the timeout elapses.
func waitForDeployment(name, namespace string, timeout time.Duration) env.Func {
	return func(ctx context.Context, c *envconf.Config) (context.Context, error) {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			out, err := exec.Command("kubectl",
				"--kubeconfig", c.KubeconfigFile(),
				"-n", namespace, "get", "deployment", name,
				"-o", "jsonpath={.status.availableReplicas}").CombinedOutput()
			if err == nil && len(out) > 0 && string(out) != "0" {
				return ctx, nil
			}
			time.Sleep(2 * time.Second)
		}
		return ctx, fmt.Errorf("deployment %s/%s not ready within %s", namespace, name, timeout)
	}
}
