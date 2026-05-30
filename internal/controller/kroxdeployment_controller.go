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
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	srcv1 "github.com/fluxcd/source-controller/api/v1"
	kroruntime "github.com/kubernetes-sigs/kro/pkg/runtime"
	v1alpha1 "github.com/trevex/krox-controller/api/v1alpha1"
	"github.com/trevex/krox-controller/internal/apply"
	"github.com/trevex/krox-controller/internal/render"
	"github.com/trevex/krox-controller/internal/source"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// KroxDeploymentReconciler reconciles a KroxDeployment.
type KroxDeploymentReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	RestConfig *rest.Config
	HTTPClient *http.Client

	Resolver *source.Resolver
	Fetcher  *source.Fetcher
	Engine   *render.Engine
	Applier  *apply.Applier
	Pruner   *apply.Pruner
}

// +kubebuilder:rbac:groups=krox.io,resources=kroxdeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=krox.io,resources=kroxdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=krox.io,resources=kroxdeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories;ocirepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;create;update;patch;delete

func (r *KroxDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var kd v1alpha1.KroxDeployment
	if err := r.Get(ctx, req.NamespacedName, &kd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if kd.Spec.Suspend {
		return ctrl.Result{}, nil
	}

	// Mark Reconciling.
	r.setCondition(&kd, v1alpha1.ConditionReconciling, metav1.ConditionTrue, "Progressing", "Reconciling")
	apimeta.RemoveStatusCondition(&kd.Status.Conditions, v1alpha1.ConditionStalled)
	if err := r.Status().Update(ctx, &kd); err != nil {
		return ctrl.Result{}, err
	}

	res, err := r.reconcile(ctx, &kd)
	if err != nil {
		logger.Error(err, "reconcile failed")
	}
	return res, err
}

func (r *KroxDeploymentReconciler) reconcile(ctx context.Context, kd *v1alpha1.KroxDeployment) (ctrl.Result, error) {
	// 1. Resolve source.
	art, err := r.Resolver.Resolve(ctx, kd.Namespace, kd.Spec.SourceRef)
	if err != nil {
		if source.IsNotReady(err) {
			apimeta.RemoveStatusCondition(&kd.Status.Conditions, v1alpha1.ConditionReconciling)
			r.setCondition(kd, v1alpha1.ConditionReady, metav1.ConditionFalse, "SourceNotReady", err.Error())
			_ = r.Status().Update(ctx, kd)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return r.terminal(ctx, kd, "SourceError", err)
	}
	kd.Status.LastAttemptedRevision = art.Revision

	// 2. Fetch artifact.
	dir, err := os.MkdirTemp("", "krox-*")
	if err != nil {
		return ctrl.Result{}, err
	}
	defer os.RemoveAll(dir)
	if err := r.Fetcher.Fetch(ctx, *art, dir); err != nil {
		return r.transient(ctx, kd, "FetchFailed", err)
	}

	// 3. Read RGD file.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return ctrl.Result{}, err
	}
	rgdPath := filepath.Join(absDir, filepath.Clean(kd.Spec.Path))
	if !strings.HasPrefix(rgdPath+string(os.PathSeparator), absDir+string(os.PathSeparator)) {
		return r.terminal(ctx, kd, "PathTraversal", fmt.Errorf("spec.path %q escapes artifact root", kd.Spec.Path))
	}
	rgdData, err := os.ReadFile(rgdPath)
	if err != nil {
		return r.terminal(ctx, kd, "RGDNotFound", err)
	}
	rgd, err := render.ParseRGD(rgdData)
	if err != nil {
		return r.terminal(ctx, kd, "RGDInvalid", err)
	}

	// 4. Build instance and plan.
	values := []byte("{}")
	if kd.Spec.Values != nil {
		values = kd.Spec.Values.Raw
	}
	inst, err := render.BuildInstance(rgd, kd.Name, kd.Namespace, values)
	if err != nil {
		return r.terminal(ctx, kd, "InstanceInvalid", err)
	}
	rt, err := r.Engine.Plan(rgd, inst)
	if err != nil {
		return r.terminal(ctx, kd, "PlanFailed", err)
	}

	// 5. Apply layer-by-layer with observe-back.
	ownerKey := fmt.Sprintf("%s/%s", kd.Namespace, kd.Name)
	newInv := &v1alpha1.ResourceInventory{}
	r.Applier.Force = kd.Spec.Force
	for _, node := range rt.Nodes() {
		ignored, err := node.IsIgnored()
		if err != nil {
			return r.terminalWithInventory(ctx, kd, newInv, "RenderFailed", err)
		}
		if ignored {
			continue
		}
		desired, err := node.GetDesired()
		if err != nil {
			return r.terminalWithInventory(ctx, kd, newInv, "RenderFailed", err)
		}
		observed := make([]*unstructured.Unstructured, 0, len(desired))
		for _, obj := range desired {
			applied, err := r.Applier.Apply(ctx, obj, ownerKey, art.Revision)
			if err != nil {
				return r.transientWithInventory(ctx, kd, newInv, "ApplyFailed", err)
			}
			newInv.Entries = append(newInv.Entries, v1alpha1.ResourceRef{
				ID: apply.IDFromObject(applied), ResourceVersion: applied.GetResourceVersion(),
			})
			observed = append(observed, applied)
		}
		node.SetObserved(observed)

		// Wait for readyWhen if this node specifies it.
		if err := node.CheckReadiness(); err != nil {
			if errors.Is(err, kroruntime.ErrWaitingForReadiness) {
				return r.transientWithInventory(ctx, kd, newInv, "WaitingForReadiness", err)
			}
			return r.terminalWithInventory(ctx, kd, newInv, "ReadinessCheckFailed", err)
		}
	}

	// 6. Prune.
	if kd.Spec.Prune {
		for _, ref := range apply.Diff(kd.Status.Inventory, newInv) {
			err := r.Pruner.Delete(ctx, ref.ID)
			if err != nil && !apierrors.IsNotFound(err) {
				return r.transient(ctx, kd, "PruneFailed", err)
			}
		}
	}

	// 7. Persist success.
	kd.Status.Inventory = newInv
	kd.Status.LastAppliedRevision = art.Revision
	kd.Status.ObservedGeneration = kd.Generation
	apimeta.RemoveStatusCondition(&kd.Status.Conditions, v1alpha1.ConditionReconciling)
	r.setCondition(kd, v1alpha1.ConditionReady, metav1.ConditionTrue, "ApplySucceeded",
		fmt.Sprintf("Applied %d resources at revision %s", len(newInv.Entries), art.Revision))
	if err := r.Status().Update(ctx, kd); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: kd.Spec.Interval.Duration}, nil
}

func (r *KroxDeploymentReconciler) setCondition(kd *v1alpha1.KroxDeployment, t string, s metav1.ConditionStatus, reason, msg string) {
	apimeta.SetStatusCondition(&kd.Status.Conditions, metav1.Condition{
		Type: t, Status: s, Reason: reason, Message: msg, ObservedGeneration: kd.Generation,
	})
}

func (r *KroxDeploymentReconciler) terminal(ctx context.Context, kd *v1alpha1.KroxDeployment, reason string, err error) (ctrl.Result, error) {
	r.setCondition(kd, v1alpha1.ConditionReady, metav1.ConditionFalse, reason, err.Error())
	r.setCondition(kd, v1alpha1.ConditionStalled, metav1.ConditionTrue, reason, err.Error())
	apimeta.RemoveStatusCondition(&kd.Status.Conditions, v1alpha1.ConditionReconciling)
	_ = r.Status().Update(ctx, kd)
	return ctrl.Result{}, nil
}

func (r *KroxDeploymentReconciler) transient(ctx context.Context, kd *v1alpha1.KroxDeployment, reason string, err error) (ctrl.Result, error) {
	apimeta.RemoveStatusCondition(&kd.Status.Conditions, v1alpha1.ConditionReconciling)
	r.setCondition(kd, v1alpha1.ConditionReady, metav1.ConditionFalse, reason, err.Error())
	_ = r.Status().Update(ctx, kd)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *KroxDeploymentReconciler) transientWithInventory(ctx context.Context, kd *v1alpha1.KroxDeployment, partialInv *v1alpha1.ResourceInventory, reason string, err error) (ctrl.Result, error) {
	// Merge partial inventory with the previous one so applied-but-not-yet-tracked
	// objects survive transient failures and can be pruned later.
	kd.Status.Inventory = mergeInventory(kd.Status.Inventory, partialInv)
	apimeta.RemoveStatusCondition(&kd.Status.Conditions, v1alpha1.ConditionReconciling)
	r.setCondition(kd, v1alpha1.ConditionReady, metav1.ConditionFalse, reason, err.Error())
	_ = r.Status().Update(ctx, kd)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *KroxDeploymentReconciler) terminalWithInventory(ctx context.Context, kd *v1alpha1.KroxDeployment, partialInv *v1alpha1.ResourceInventory, reason string, err error) (ctrl.Result, error) {
	kd.Status.Inventory = mergeInventory(kd.Status.Inventory, partialInv)
	return r.terminal(ctx, kd, reason, err)
}

func mergeInventory(prev, partial *v1alpha1.ResourceInventory) *v1alpha1.ResourceInventory {
	seen := map[string]struct{}{}
	out := &v1alpha1.ResourceInventory{}
	// partial first — most recent versions take precedence in append order
	if partial != nil {
		for _, r := range partial.Entries {
			if _, ok := seen[r.ID]; ok {
				continue
			}
			seen[r.ID] = struct{}{}
			out.Entries = append(out.Entries, r)
		}
	}
	if prev != nil {
		for _, r := range prev.Entries {
			if _, ok := seen[r.ID]; ok {
				continue
			}
			seen[r.ID] = struct{}{}
			out.Entries = append(out.Entries, r)
		}
	}
	return out
}

func (r *KroxDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapFn := func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.KroxDeploymentList
		if err := mgr.GetClient().List(ctx, &list); err != nil {
			return nil
		}
		ns := obj.GetNamespace()
		name := obj.GetName()
		var out []reconcile.Request
		for _, kd := range list.Items {
			srcNs := kd.Spec.SourceRef.Namespace
			if srcNs == "" {
				srcNs = kd.Namespace
			}
			if srcNs == ns && kd.Spec.SourceRef.Name == name {
				out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: kd.Namespace, Name: kd.Name}})
			}
		}
		return out
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.KroxDeployment{}).
		Watches(&srcv1.GitRepository{}, handler.EnqueueRequestsFromMapFunc(mapFn)).
		Watches(&srcv1.OCIRepository{}, handler.EnqueueRequestsFromMapFunc(mapFn)).
		Complete(r)
}
