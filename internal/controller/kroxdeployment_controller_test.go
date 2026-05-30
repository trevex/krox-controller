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

package controller

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	srcv1 "github.com/fluxcd/source-controller/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1alpha1 "github.com/trevex/krox-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("KroxDeployment reconcile", func() {
	const ns = "default"

	It("renders an RGD into a Deployment and reports Ready", func() {
		rgd, err := os.ReadFile("../../test/testdata/rgds/webapp.yaml")
		Expect(err).NotTo(HaveOccurred())
		tgz := tarGz(map[string]string{"rgd.yaml": string(rgd)})
		digest := "sha256:" + sha256Hex(tgz)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(tgz)
		}))
		DeferCleanup(srv.Close)

		gr := &srcv1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "src", Namespace: ns},
			Spec: srcv1.GitRepositorySpec{
				Interval: metav1.Duration{Duration: time.Minute},
				URL:      "https://example.invalid",
			},
		}
		Expect(k8sClient.Create(ctx, gr)).To(Succeed())

		gr.Status = srcv1.GitRepositoryStatus{
			Conditions: []metav1.Condition{{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "Succeeded",
				Message:            "ok",
			}},
			Artifact: makeArtifact(srv.URL, "main@sha:abc", digest),
		}
		Expect(k8sClient.Status().Update(ctx, gr)).To(Succeed())

		kd := &v1alpha1.KroxDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: "wa", Namespace: ns},
			Spec: v1alpha1.KroxDeploymentSpec{
				Interval:  metav1.Duration{Duration: 30 * time.Second},
				SourceRef: v1alpha1.SourceReference{Kind: "GitRepository", Name: "src"},
				Path:      "rgd.yaml",
				Values:    &apiextensionsv1.JSON{Raw: []byte(`{"name":"web","replicas":2,"image":"nginx:1.27"}`)},
				Prune:     true,
			},
		}
		Expect(k8sClient.Create(ctx, kd)).To(Succeed())

		Eventually(func(g Gomega) {
			out := &v1alpha1.KroxDeployment{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "wa", Namespace: ns}, out)).To(Succeed())
			cond := findCondition(out.Status.Conditions, v1alpha1.ConditionReady)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(out.Status.LastAppliedRevision).To(Equal("main@sha:abc"))
			g.Expect(out.Status.Inventory).NotTo(BeNil())
			g.Expect(out.Status.Inventory.Entries).To(HaveLen(1))
		}, "60s", "1s").Should(Succeed())

		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "web", Namespace: ns}, dep)).To(Succeed())
		Expect(dep.Spec.Replicas).NotTo(BeNil())
		Expect(*dep.Spec.Replicas).To(BeNumerically("==", 2))
	})
})

func findCondition(cs []metav1.Condition, t string) *metav1.Condition {
	for i := range cs {
		if cs[i].Type == t {
			return &cs[i]
		}
	}
	return nil
}

func makeArtifact(url, revision, digest string) *fluxmeta.Artifact {
	return &fluxmeta.Artifact{
		URL:            url,
		Revision:       revision,
		Digest:         digest,
		Path:           "artifact.tar.gz",
		LastUpdateTime: metav1.Now(),
	}
}

func tarGz(entries map[string]string) []byte {
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

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var _ = Describe("KroxDeployment failure modes", func() {
	const ns = "default"

	It("waits when source is not ready (transient)", func() {
		gr := &srcv1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "pending-src", Namespace: ns},
			Spec:       srcv1.GitRepositorySpec{URL: "https://x", Interval: metav1.Duration{Duration: time.Minute}},
		}
		Expect(k8sClient.Create(ctx, gr)).To(Succeed())
		gr.Status = srcv1.GitRepositoryStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, LastTransitionTime: metav1.Now(), Reason: "Progressing", Message: "fetching"}},
		}
		Expect(k8sClient.Status().Update(ctx, gr)).To(Succeed())

		kd := &v1alpha1.KroxDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: "wait", Namespace: ns},
			Spec: v1alpha1.KroxDeploymentSpec{
				Interval:  metav1.Duration{Duration: 30 * time.Second},
				SourceRef: v1alpha1.SourceReference{Kind: "GitRepository", Name: "pending-src"},
				Path:      "rgd.yaml",
			},
		}
		Expect(k8sClient.Create(ctx, kd)).To(Succeed())

		Eventually(func(g Gomega) {
			out := &v1alpha1.KroxDeployment{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "wait", Namespace: ns}, out)).To(Succeed())
			cond := findCondition(out.Status.Conditions, v1alpha1.ConditionReady)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal("SourceNotReady"))
			// Should NOT be Stalled — transient.
			stalled := findCondition(out.Status.Conditions, v1alpha1.ConditionStalled)
			g.Expect(stalled).To(BeNil())
		}, "30s", "1s").Should(Succeed())
	})

	It("marks Stalled on RGD parse error (terminal)", func() {
		bad := []byte("not a valid: : rgd")
		tgz := tarGz(map[string]string{"rgd.yaml": string(bad)})
		digest := "sha256:" + sha256Hex(tgz)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(tgz) }))
		DeferCleanup(srv.Close)

		gr := &srcv1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-src", Namespace: ns},
			Spec:       srcv1.GitRepositorySpec{URL: "https://x", Interval: metav1.Duration{Duration: time.Minute}},
		}
		Expect(k8sClient.Create(ctx, gr)).To(Succeed())
		gr.Status = srcv1.GitRepositoryStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, LastTransitionTime: metav1.Now(), Reason: "Test"}},
			Artifact:   makeArtifact(srv.URL, "v1", digest),
		}
		Expect(k8sClient.Status().Update(ctx, gr)).To(Succeed())

		kd := &v1alpha1.KroxDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: ns},
			Spec: v1alpha1.KroxDeploymentSpec{
				Interval:  metav1.Duration{Duration: time.Minute},
				SourceRef: v1alpha1.SourceReference{Kind: "GitRepository", Name: "bad-src"},
				Path:      "rgd.yaml",
			},
		}
		Expect(k8sClient.Create(ctx, kd)).To(Succeed())

		Eventually(func(g Gomega) {
			out := &v1alpha1.KroxDeployment{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "bad", Namespace: ns}, out)).To(Succeed())
			stalled := findCondition(out.Status.Conditions, v1alpha1.ConditionStalled)
			g.Expect(stalled).NotTo(BeNil())
			g.Expect(stalled.Status).To(Equal(metav1.ConditionTrue))
		}, "30s", "1s").Should(Succeed())
	})
})

var _ = Describe("KroxDeployment prune", func() {
	const ns = "default"

	It("deletes resources removed from the RGD", func() {
		// Serve dynamic content: start with two-resource RGD, then swap.
		rgdWithSvc, err := os.ReadFile("../../test/testdata/rgds/webapp-with-svc.yaml")
		Expect(err).NotTo(HaveOccurred())
		rgdNoSvc, err := os.ReadFile("../../test/testdata/rgds/webapp-svc-removed.yaml")
		Expect(err).NotTo(HaveOccurred())

		serveRgd := rgdWithSvc
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tgz := tarGz(map[string]string{"rgd.yaml": string(serveRgd)})
			_, _ = w.Write(tgz)
		}))
		DeferCleanup(srv.Close)

		makeRevDigest := func(body []byte, rev string) (string, string) {
			tgz := tarGz(map[string]string{"rgd.yaml": string(body)})
			return rev, "sha256:" + sha256Hex(tgz)
		}
		rev1, digest1 := makeRevDigest(rgdWithSvc, "rev1")

		gr := &srcv1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "prune-src", Namespace: ns},
			Spec:       srcv1.GitRepositorySpec{URL: "https://x", Interval: metav1.Duration{Duration: time.Minute}},
		}
		Expect(k8sClient.Create(ctx, gr)).To(Succeed())
		gr.Status = srcv1.GitRepositoryStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, LastTransitionTime: metav1.Now(), Reason: "OK"}},
			Artifact:   makeArtifact(srv.URL, rev1, digest1),
		}
		Expect(k8sClient.Status().Update(ctx, gr)).To(Succeed())

		kd := &v1alpha1.KroxDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: "prune-kd", Namespace: ns},
			Spec: v1alpha1.KroxDeploymentSpec{
				Interval:  metav1.Duration{Duration: 30 * time.Second},
				SourceRef: v1alpha1.SourceReference{Kind: "GitRepository", Name: "prune-src"},
				Path:      "rgd.yaml",
				Values:    &apiextensionsv1.JSON{Raw: []byte(`{"name":"prunable","replicas":1,"image":"nginx:1.27"}`)},
				Prune:     true,
			},
		}
		Expect(k8sClient.Create(ctx, kd)).To(Succeed())

		// Wait for both to exist and Ready=True with 2 inventory entries.
		Eventually(func(g Gomega) {
			out := &v1alpha1.KroxDeployment{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "prune-kd", Namespace: ns}, out)).To(Succeed())
			cond := findCondition(out.Status.Conditions, v1alpha1.ConditionReady)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(out.Status.Inventory).NotTo(BeNil())
			g.Expect(out.Status.Inventory.Entries).To(HaveLen(2))
		}, "60s", "1s").Should(Succeed())

		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "prunable-svc", Namespace: ns}, svc)).To(Succeed())

		// Swap to RGD without service + bump revision/digest.
		serveRgd = rgdNoSvc
		rev2, digest2 := makeRevDigest(rgdNoSvc, "rev2")
		Eventually(func() error {
			latest := &srcv1.GitRepository{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "prune-src", Namespace: ns}, latest); err != nil {
				return err
			}
			latest.Status.Artifact = makeArtifact(srv.URL, rev2, digest2)
			return k8sClient.Status().Update(ctx, latest)
		}, "10s", "1s").Should(Succeed())

		// Eventually the Service is gone (or pending deletion) and Inventory has only 1 entry.
		Eventually(func(g Gomega) {
			out := &v1alpha1.KroxDeployment{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "prune-kd", Namespace: ns}, out)).To(Succeed())
			g.Expect(out.Status.LastAppliedRevision).To(Equal("rev2"))
			g.Expect(out.Status.Inventory).NotTo(BeNil())
			g.Expect(out.Status.Inventory.Entries).To(HaveLen(1))

			// Service should be gone, or at least have a deletion timestamp set
			// (envtest has no kube-controller-manager so finalizers may not be processed).
			svc := &corev1.Service{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "prunable-svc", Namespace: ns}, svc)
			if err == nil {
				g.Expect(svc.GetDeletionTimestamp()).NotTo(BeNil())
				return
			}
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}, "90s", "2s").Should(Succeed())
	})
})
