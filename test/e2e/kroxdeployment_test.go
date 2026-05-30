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

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	srcv1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/stretchr/testify/require"
	v1alpha1 "github.com/trevex/krox-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// fixtureNamespace is where the user-facing objects (KroxDeployment,
// GitRepository, fixture HTTP server) live for the happy-path test.
const fixtureNamespace = "default"

// fixtureServiceName is the in-cluster Service name that serves the RGD
// tarball and is referenced by the GitRepository artifact URL.
const fixtureServiceName = "fixture-src"

// TestRenderAndApply exercises the full controller pipeline against a kind
// cluster:
//  1. Build a deterministic tar.gz of the RGD on disk.
//  2. Serve it from an in-cluster busybox httpd backed by a ConfigMap (so the
//     controller can fetch http://fixture-src.default... and verify the
//     sha256 digest end-to-end).
//  3. Create a suspended GitRepository whose .status.artifact points at the
//     fixture server. Suspend keeps source-controller from clobbering the
//     status we patch in.
//  4. Create a KroxDeployment that references the GitRepository.
//  5. Assert: Ready=True and the rendered Deployment "web" exists.
func TestRenderAndApply(t *testing.T) {
	rgd, err := os.ReadFile("../testdata/rgds/webapp.yaml")
	require.NoError(t, err, "read RGD fixture")
	tgz := makeTarGz(map[string]string{"rgd.yaml": string(rgd)})
	digest := "sha256:" + hexSha256(tgz)

	f := features.New("render-and-apply").
		Setup(deployFixtureServer(tgz)).
		Setup(createSuspendedGitRepoStatus(digest)).
		Assess("KroxDeployment becomes Ready and Deployment web exists", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			cli := mustClient(t, c)

			kd := &v1alpha1.KroxDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "webapp", Namespace: fixtureNamespace},
				Spec: v1alpha1.KroxDeploymentSpec{
					Interval:  metav1.Duration{Duration: 30 * time.Second},
					SourceRef: v1alpha1.SourceReference{Kind: "GitRepository", Name: fixtureServiceName},
					Path:      "rgd.yaml",
					Values:    &apiextensionsv1.JSON{Raw: []byte(`{"name":"web","replicas":1,"image":"nginx:1.27"}`)},
					Prune:     true,
				},
			}
			require.NoError(t, cli.Create(ctx, kd))

			require.Eventually(t, func() bool {
				out := &v1alpha1.KroxDeployment{}
				if err := cli.Get(ctx, types.NamespacedName{Name: "webapp", Namespace: fixtureNamespace}, out); err != nil {
					return false
				}
				for _, cond := range out.Status.Conditions {
					if cond.Type == v1alpha1.ConditionReady && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, 4*time.Minute, 3*time.Second, "KroxDeployment never became Ready")

			dep := &appsv1.Deployment{}
			require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: "web", Namespace: fixtureNamespace}, dep))
			require.NotNil(t, dep.Spec.Replicas)
			require.Equal(t, int32(1), *dep.Spec.Replicas)
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

// mustClient builds a controller-runtime client wired with the v1alpha1 +
// source-controller schemes so the test can create/read those types directly.
func mustClient(t *testing.T, c *envconf.Config) client.Client {
	t.Helper()
	require.NoError(t, srcv1.AddToScheme(c.Client().Resources().GetScheme()))
	require.NoError(t, v1alpha1.AddToScheme(c.Client().Resources().GetScheme()))
	cli, err := client.New(c.Client().RESTConfig(), client.Options{Scheme: c.Client().Resources().GetScheme()})
	require.NoError(t, err)
	return cli
}

// deployFixtureServer creates a ConfigMap (with the tarball as binaryData),
// a busybox httpd Deployment that serves it, and a Service named fixture-src.
// The kubelet decodes binaryData back to raw bytes when mounting, so the
// tarball bytes the controller fetches equal the bytes we hashed locally.
func deployFixtureServer(tgz []byte) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Helper()
		cli := mustClient(t, c)

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "fixture-tgz", Namespace: fixtureNamespace},
			BinaryData: map[string][]byte{"rgd.tgz": tgz},
		}
		require.NoError(t, cli.Create(ctx, cm))

		replicas := int32(1)
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: fixtureServiceName, Namespace: fixtureNamespace},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": fixtureServiceName}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": fixtureServiceName}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:         "httpd",
							Image:        "busybox:1.36",
							Command:      []string{"sh", "-c", "cd /www && exec busybox httpd -f -p 80"},
							VolumeMounts: []corev1.VolumeMount{{Name: "www", MountPath: "/www"}},
							Ports:        []corev1.ContainerPort{{ContainerPort: 80}},
						}},
						Volumes: []corev1.Volume{{
							Name: "www",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "fixture-tgz"},
								},
							},
						}},
					},
				},
			},
		}
		require.NoError(t, cli.Create(ctx, dep))

		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: fixtureServiceName, Namespace: fixtureNamespace},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": fixtureServiceName},
				Ports: []corev1.ServicePort{{
					Port: 80, TargetPort: intstr.FromInt(80), Protocol: corev1.ProtocolTCP,
				}},
			},
		}
		require.NoError(t, cli.Create(ctx, svc))

		require.Eventually(t, func() bool {
			d := &appsv1.Deployment{}
			if err := cli.Get(ctx, types.NamespacedName{Name: fixtureServiceName, Namespace: fixtureNamespace}, d); err != nil {
				return false
			}
			return d.Status.AvailableReplicas > 0
		}, 3*time.Minute, 3*time.Second, "fixture-src Deployment never became Available")
		return ctx
	}
}

// createSuspendedGitRepoStatus creates a suspended GitRepository (so source-
// controller does NOT try to clone the bogus URL) and patches its status to
// point at the in-cluster fixture-src Service.
func createSuspendedGitRepoStatus(digest string) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Helper()
		cli := mustClient(t, c)
		gr := &srcv1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{Name: fixtureServiceName, Namespace: fixtureNamespace},
			Spec: srcv1.GitRepositorySpec{
				URL:      "https://example.invalid/fixture.git",
				Interval: metav1.Duration{Duration: time.Hour},
				Suspend:  true,
			},
		}
		require.NoError(t, cli.Create(ctx, gr))
		// Retry the status update; source-controller may race us by updating
		// the GitRepository status (even though suspended) before our patch
		// lands. RetryOnConflict re-fetches and re-applies the desired status.
		require.NoError(t, retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			cur := &srcv1.GitRepository{}
			if err := cli.Get(ctx, types.NamespacedName{Name: fixtureServiceName, Namespace: fixtureNamespace}, cur); err != nil {
				return err
			}
			cur.Status = srcv1.GitRepositoryStatus{
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					Reason:             "Test",
					Message:            "fixture status patched by e2e test",
				}},
				Artifact: &fluxmeta.Artifact{
					URL:            "http://" + fixtureServiceName + ".default.svc.cluster.local/rgd.tgz",
					Path:           "rgd.tgz",
					Revision:       "v1",
					Digest:         digest,
					LastUpdateTime: metav1.Now(),
				},
			}
			return cli.Status().Update(ctx, cur)
		}))
		return ctx
	}
}

// makeTarGz builds a deterministic tar.gz from the entries map.
func makeTarGz(entries map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func hexSha256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestPrune exercises the prune path against a kind cluster:
//  1. Start with an RGD that renders BOTH a Deployment and a Service.
//  2. Wait for Ready and assert both resources exist and Inventory has 2 entries.
//  3. Swap the in-cluster ConfigMap to an RGD that only renders the Deployment,
//     restart the fixture-server pod (so the new tarball is mounted), and bump
//     the GitRepository's artifact revision/digest.
//  4. Assert the Service is pruned, the Deployment remains, Inventory has 1
//     entry, and LastAppliedRevision advanced to v2.
//
// Uses distinct resource names from TestRenderAndApply so the two tests can
// run sequentially in the same suite without colliding.
func TestPrune(t *testing.T) {
	rgdWithSvc, err := os.ReadFile("../testdata/rgds/webapp-with-svc.yaml")
	if err != nil {
		rgdWithSvc, err = os.ReadFile("../../test/testdata/rgds/webapp-with-svc.yaml")
	}
	require.NoError(t, err, "read webapp-with-svc.yaml")
	rgdNoSvc, err := os.ReadFile("../testdata/rgds/webapp-svc-removed.yaml")
	if err != nil {
		rgdNoSvc, err = os.ReadFile("../../test/testdata/rgds/webapp-svc-removed.yaml")
	}
	require.NoError(t, err, "read webapp-svc-removed.yaml")

	tgzWith := makeTarGz(map[string]string{"rgd.yaml": string(rgdWithSvc)})
	tgzWithout := makeTarGz(map[string]string{"rgd.yaml": string(rgdNoSvc)})
	digestWith := "sha256:" + hexSha256(tgzWith)
	digestWithout := "sha256:" + hexSha256(tgzWithout)

	const (
		cmName  = "fixture-tgz-prune"
		svcName = "fixture-src-prune"
		grName  = "fixture-src-prune"
		kdName  = "webapp-prune"
		appName = "prune-app"
		svcKind = appName + "-svc"
	)

	f := features.New("prune").
		Setup(deployFixtureServerNamed(tgzWith, cmName, svcName)).
		Setup(createSuspendedGitRepoStatusNamed(grName, svcName, "v1", digestWith)).
		Assess("KroxDeployment Ready and both resources exist", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			cli := mustClient(t, c)

			kd := &v1alpha1.KroxDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: kdName, Namespace: fixtureNamespace},
				Spec: v1alpha1.KroxDeploymentSpec{
					Interval:  metav1.Duration{Duration: 30 * time.Second},
					SourceRef: v1alpha1.SourceReference{Kind: "GitRepository", Name: grName},
					Path:      "rgd.yaml",
					Values:    &apiextensionsv1.JSON{Raw: []byte(`{"name":"` + appName + `","replicas":1,"image":"nginx:1.27"}`)},
					Prune:     true,
				},
			}
			require.NoError(t, cli.Create(ctx, kd))

			require.Eventually(t, func() bool {
				out := &v1alpha1.KroxDeployment{}
				if err := cli.Get(ctx, types.NamespacedName{Name: kdName, Namespace: fixtureNamespace}, out); err != nil {
					return false
				}
				if out.Status.Inventory == nil || len(out.Status.Inventory.Entries) != 2 {
					return false
				}
				for _, cond := range out.Status.Conditions {
					if cond.Type == v1alpha1.ConditionReady && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, 4*time.Minute, 3*time.Second, "KroxDeployment never became Ready with 2 inventory entries")

			dep := &appsv1.Deployment{}
			require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: appName, Namespace: fixtureNamespace}, dep))
			svc := &corev1.Service{}
			require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: svcKind, Namespace: fixtureNamespace}, svc))
			return ctx
		}).
		Assess("Service is pruned when removed from RGD", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			cli := mustClient(t, c)

			// Swap the ConfigMap content (kubelet will eventually re-mount).
			cm := &corev1.ConfigMap{}
			require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: cmName, Namespace: fixtureNamespace}, cm))
			cm.BinaryData = map[string][]byte{"rgd.tgz": tgzWithout}
			require.NoError(t, cli.Update(ctx, cm))

			// Force a deterministic re-mount of the new ConfigMap content by
			// restarting the fixture-server pod. kubelet's default ConfigMap
			// sync period is ~60s; rollout restart is faster and predictable.
			require.NoError(t, kubectlRolloutRestart(c, "deployment/"+svcName, fixtureNamespace))
			require.NoError(t, kubectlRolloutStatus(c, "deployment/"+svcName, fixtureNamespace, 2*time.Minute))

			// Bump the GitRepository artifact rev + digest so the controller
			// picks up the new tarball on its next reconcile.
			require.NoError(t, retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				gr := &srcv1.GitRepository{}
				if err := cli.Get(ctx, types.NamespacedName{Name: grName, Namespace: fixtureNamespace}, gr); err != nil {
					return err
				}
				gr.Status.Artifact = &fluxmeta.Artifact{
					URL:            "http://" + svcName + ".default.svc.cluster.local/rgd.tgz",
					Path:           "rgd.tgz",
					Revision:       "v2",
					Digest:         digestWithout,
					LastUpdateTime: metav1.Now(),
				}
				return cli.Status().Update(ctx, gr)
			}))

			// Eventually: LastAppliedRevision advances, Inventory shrinks to 1,
			// and the Service is gone.
			require.Eventually(t, func() bool {
				out := &v1alpha1.KroxDeployment{}
				if err := cli.Get(ctx, types.NamespacedName{Name: kdName, Namespace: fixtureNamespace}, out); err != nil {
					return false
				}
				if out.Status.LastAppliedRevision != "v2" {
					return false
				}
				if out.Status.Inventory == nil || len(out.Status.Inventory.Entries) != 1 {
					return false
				}
				svc := &corev1.Service{}
				err := cli.Get(ctx, types.NamespacedName{Name: svcKind, Namespace: fixtureNamespace}, svc)
				// Expect NotFound (or any error indicating absence).
				return err != nil
			}, 4*time.Minute, 3*time.Second, "Service was not pruned after RGD swap")

			// Sanity: Deployment must still exist.
			dep := &appsv1.Deployment{}
			require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: appName, Namespace: fixtureNamespace}, dep))
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

// deployFixtureServerNamed mirrors deployFixtureServer but with configurable
// resource names so multiple tests can run side by side without collisions.
func deployFixtureServerNamed(tgz []byte, cmName, svcName string) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Helper()
		cli := mustClient(t, c)

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: fixtureNamespace},
			BinaryData: map[string][]byte{"rgd.tgz": tgz},
		}
		require.NoError(t, cli.Create(ctx, cm))

		replicas := int32(1)
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: svcName, Namespace: fixtureNamespace},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": svcName}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": svcName}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:         "httpd",
							Image:        "busybox:1.36",
							Command:      []string{"sh", "-c", "cd /www && exec busybox httpd -f -p 80"},
							VolumeMounts: []corev1.VolumeMount{{Name: "www", MountPath: "/www"}},
							Ports:        []corev1.ContainerPort{{ContainerPort: 80}},
						}},
						Volumes: []corev1.Volume{{
							Name: "www",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
								},
							},
						}},
					},
				},
			},
		}
		require.NoError(t, cli.Create(ctx, dep))

		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: svcName, Namespace: fixtureNamespace},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": svcName},
				Ports: []corev1.ServicePort{{
					Port: 80, TargetPort: intstr.FromInt(80), Protocol: corev1.ProtocolTCP,
				}},
			},
		}
		require.NoError(t, cli.Create(ctx, svc))

		require.Eventually(t, func() bool {
			d := &appsv1.Deployment{}
			if err := cli.Get(ctx, types.NamespacedName{Name: svcName, Namespace: fixtureNamespace}, d); err != nil {
				return false
			}
			return d.Status.AvailableReplicas > 0
		}, 3*time.Minute, 3*time.Second, "fixture Deployment "+svcName+" never became Available")
		return ctx
	}
}

// createSuspendedGitRepoStatusNamed mirrors createSuspendedGitRepoStatus with
// configurable GitRepository / Service names and an explicit revision.
func createSuspendedGitRepoStatusNamed(grName, svcName, revision, digest string) func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Helper()
		cli := mustClient(t, c)
		gr := &srcv1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{Name: grName, Namespace: fixtureNamespace},
			Spec: srcv1.GitRepositorySpec{
				URL:      "https://example.invalid/fixture.git",
				Interval: metav1.Duration{Duration: time.Hour},
				Suspend:  true,
			},
		}
		require.NoError(t, cli.Create(ctx, gr))
		require.NoError(t, retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			cur := &srcv1.GitRepository{}
			if err := cli.Get(ctx, types.NamespacedName{Name: grName, Namespace: fixtureNamespace}, cur); err != nil {
				return err
			}
			cur.Status = srcv1.GitRepositoryStatus{
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					Reason:             "Test",
					Message:            "fixture status patched by e2e prune test",
				}},
				Artifact: &fluxmeta.Artifact{
					URL:            "http://" + svcName + ".default.svc.cluster.local/rgd.tgz",
					Path:           "rgd.tgz",
					Revision:       revision,
					Digest:         digest,
					LastUpdateTime: metav1.Now(),
				},
			}
			return cli.Status().Update(ctx, cur)
		}))
		return ctx
	}
}

// kubectlRolloutRestart shells out to `kubectl rollout restart` to force a
// pod rotation. Used to deterministically re-mount a swapped ConfigMap rather
// than waiting for kubelet's periodic sync.
func kubectlRolloutRestart(c *envconf.Config, target, ns string) error {
	out, err := exec.Command("kubectl", "--kubeconfig", c.KubeconfigFile(), "-n", ns, "rollout", "restart", target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rollout restart %s: %w\n%s", target, err, string(out))
	}
	return nil
}

// kubectlRolloutStatus waits for `kubectl rollout status` to converge.
func kubectlRolloutStatus(c *envconf.Config, target, ns string, timeout time.Duration) error {
	out, err := exec.Command("kubectl", "--kubeconfig", c.KubeconfigFile(), "-n", ns, "rollout", "status", target, "--timeout="+timeout.String()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rollout status %s: %w\n%s", target, err, string(out))
	}
	return nil
}
