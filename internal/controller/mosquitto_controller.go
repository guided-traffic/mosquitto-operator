// Package controller implements the reconciliation logic for Mosquitto custom
// resources.
package controller

import (
	"context"
	"fmt"
	"io"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/builder"
	"github.com/guided-traffic/mosquitto-operator/internal/common"
)

// DefaultMaxConcurrentReconciles is how many Mosquitto resources the operator
// reconciles at the same time when the flag is not set.
//
// controller-runtime defaults this to 1, which couples every resource in the
// cluster to the slowest pass. Passes for the same resource stay serialised at
// any value, because the work queue never runs two passes for one key.
const DefaultMaxConcurrentReconciles = 4

// MosquittoReconciler reconciles a Mosquitto object.
type MosquittoReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// MaxConcurrentReconciles is how many Mosquitto resources are reconciled at
	// the same time. Zero means DefaultMaxConcurrentReconciles.
	MaxConcurrentReconciles int

	// SecretSecurity is --secret-security: a TLS Secret is mounted only when it
	// carries mkov1.ConsumableLabel (ADR 0014 D10).
	SecretSecurity bool

	// APIReader is the manager's uncached reader. Every read of Secret data goes
	// through it: the manager caches Secrets with their data stripped
	// (StripSecret), so the cache holds no credential, and a Secret's data is
	// fetched only when a pass needs it. Nil falls back to Client.
	APIReader client.Reader

	// ReloaderImage is --reloader-image, the operator's own image, which runs
	// auth-init and the reloader in every broker pod (ADR 0014 D6).
	ReloaderImage string

	// SecretNamespaces is --secret-namespaces: empty means the operator may read
	// and write Secrets in every namespace; otherwise only in these, and a
	// Mosquitto elsewhere is refused (ADR 0014 D7).
	SecretNamespaces []string

	// Random is the source of password salts; nil means crypto/rand.
	Random io.Reader
}

// These markers are the only source of the ClusterRole, and the ClusterRole is
// cluster-wide: every verb here is granted on every namespace. Each one is
// therefore justified below, because an unused cluster-wide verb is blast radius
// nobody chose.
//
// get, create and update name calls this reconciler makes directly. list and
// watch do not: they are what controller-runtime's informer cache needs to cache
// a type at all, so they are required for every kind the manager watches even
// though no line of this file calls List.
//
// Deliberately absent: delete, because tearing the owned objects down is the
// garbage collector's job through the owner references; and patch, because
// nothing here patches — a grep for `.Patch(` over the non-test tree returns
// nothing. If server-side apply is ever adopted, patch comes back with the code
// that needs it, not before.
//
// +kubebuilder:rbac:groups=mko.gtrfc.com,resources=mosquittoes,verbs=get;list;watch
// The status subresource is written with Status().Update(), which is a PUT. It is
// never read on its own — the reconciler reads status off the object it already
// fetched — so this rule is update and nothing else.
// +kubebuilder:rbac:groups=mko.gtrfc.com,resources=mosquittoes/status,verbs=update
// The owner references the reconciler writes carry blockOwnerDeletion, and the
// OwnerReferencesPermissionEnforcement admission plugin — off by default, on in
// some managed distributions — rejects such a reference unless the writer may
// update the owner's finalizers. Without this rule the operator would create
// nothing on those clusters.
// +kubebuilder:rbac:groups=mko.gtrfc.com,resources=mosquittoes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update
// The users are read and get their status written; the reconciler never writes
// a MosquittoUser's spec (ADR 0006 D9).
// +kubebuilder:rbac:groups=mko.gtrfc.com,resources=mosquittousers,verbs=get;list;watch
// +kubebuilder:rbac:groups=mko.gtrfc.com,resources=mosquittousers/status,verbs=update
// Secrets: get, list and watch read the users' credentials Secrets and wake a
// pass when one changes - the cache keeps their metadata only, the data is read
// with get when a pass needs it; create and update write the one rendered
// Secret <name>-auth per broker (ADR 0014 D2, D7). Still no delete and no patch:
// <name>-auth is collected with its Mosquitto. With --secret-namespaces the
// install path grants this rule per namespace instead, and the chart and the
// kustomize component remove it from the ClusterRole.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update

// Reconcile handles one reconciliation request for a Mosquitto resource.
func (r *MosquittoReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	m := &mkov1.Mosquitto{}
	if err := r.Get(ctx, req.NamespacedName, m); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Mosquitto resource not found, probably deleted")
			return ctrl.Result{}, r.reportUsersOfMissingBroker(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}

	// Nothing is reconciled for a resource that is going away: the owner
	// references make garbage collection delete every managed object, and writing
	// them again here would race that deletion.
	if !m.DeletionTimestamp.IsZero() {
		logger.Info("Mosquitto resource is being deleted, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	refused, err := r.refusePass(ctx, m)
	accepted := 0
	if err == nil && !refused {
		accepted, err = r.reconcileResources(ctx, m)
	}
	if err != nil {
		// The failure is reported on the resource before it is returned, so a
		// rejected write is visible with kubectl get instead of only in the
		// operator log. The error is still returned so the work queue backs the
		// retry off.
		r.setPhase(m, mkov1.PhaseFailed, metav1.ConditionFalse, "ReconcileFailed", err.Error())
		if statusErr := r.persistStatus(ctx, m); statusErr != nil {
			logger.Error(statusErr, "Failed to record the reconcile failure on the resource")
		}
		return ctrl.Result{}, err
	}
	if refused {
		return ctrl.Result{}, r.persistStatus(ctx, m)
	}

	setUsersCondition(m, accepted)
	return ctrl.Result{}, r.updateStatus(ctx, m)
}

// reconcileResources renders the users and writes every object the Mosquitto
// owns, in dependency order: the pods mount <name>-auth and the ConfigMap and
// are addressed through the headless Service, so all of those exist before the
// StatefulSet does. The users' statuses are written once their credentials are,
// so Ready on a user means it is in <name>-auth. It returns how many users the
// broker accepts.
func (r *MosquittoReconciler) reconcileResources(ctx context.Context, m *mkov1.Mosquitto) (int, error) {
	pass, err := r.renderUsers(ctx, m)
	if err != nil {
		return 0, err
	}
	if err := r.reconcileAuthSecret(ctx, m, pass.data); err != nil {
		return 0, err
	}
	if err := r.writeUserStatuses(ctx, pass); err != nil {
		return 0, err
	}
	if err := r.reconcileConfigMap(ctx, m); err != nil {
		return 0, err
	}
	if err := r.reconcileService(ctx, m, builder.BuildHeadlessService(m)); err != nil {
		return 0, err
	}
	if err := r.reconcileService(ctx, m, builder.BuildClientService(m)); err != nil {
		return 0, err
	}
	return pass.accepted, r.reconcileStatefulSet(ctx, m)
}

// reconcileConfigMap ensures the ConfigMap exists and carries the generated
// configuration.
func (r *MosquittoReconciler) reconcileConfigMap(ctx context.Context, m *mkov1.Mosquitto) error {
	logger := log.FromContext(ctx)
	desired := builder.BuildConfigMap(m)

	if err := controllerutil.SetControllerReference(m, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on ConfigMap %s: %w", desired.Name, err)
	}

	current := &corev1.ConfigMap{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		logger.Info("Creating ConfigMap", "name", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if err := ensureOwned(current, m, "ConfigMap"); err != nil {
		return err
	}

	if equality.Semantic.DeepEqual(current.Data, desired.Data) &&
		!common.MapEntriesMissing(desired.Labels, current.Labels) {
		return nil
	}

	logger.Info("Updating ConfigMap", "name", desired.Name)
	current.Data = desired.Data
	// Merged, not assigned: labels other writers added stay (ADR 0009 D9).
	current.Labels = common.MergeLabels(current.Labels, desired.Labels)
	return r.Update(ctx, current)
}

// reconcileService ensures one Service exists and matches the desired ports,
// selector and labels.
//
// The spec.selector of a live Service is mutable, so — unlike the StatefulSet,
// where the immutable fields would reject the write — nothing stops the operator
// from pointing a Service somebody else created at these pods. The ownership
// check is that stop.
func (r *MosquittoReconciler) reconcileService(ctx context.Context, m *mkov1.Mosquitto, desired *corev1.Service) error {
	logger := log.FromContext(ctx)

	if err := controllerutil.SetControllerReference(m, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on Service %s: %w", desired.Name, err)
	}

	current := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		logger.Info("Creating Service", "name", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if err := ensureOwned(current, m, "Service"); err != nil {
		return err
	}

	if equality.Semantic.DeepEqual(current.Spec.Ports, desired.Spec.Ports) &&
		equality.Semantic.DeepEqual(current.Spec.Selector, desired.Spec.Selector) &&
		!common.MapEntriesMissing(desired.Labels, current.Labels) {
		return nil
	}

	logger.Info("Updating Service", "name", desired.Name)
	current.Spec.Ports = desired.Spec.Ports
	current.Spec.Selector = desired.Spec.Selector
	// Merged, not assigned: labels other writers added stay (ADR 0009 D9).
	current.Labels = common.MergeLabels(current.Labels, desired.Labels)
	return r.Update(ctx, current)
}

// reconcileStatefulSet ensures the broker StatefulSet exists and matches the spec.
//
// Only the replica count, the pod template, the labels and the annotations are
// written, merged rather than assigned (builder.MergeStatefulSet). The
// volumeClaimTemplates are left alone on purpose: they are immutable, so writing
// them back would either be a no-op or a rejected request, and a changed
// spec.storage needs the StatefulSet recreated by hand.
func (r *MosquittoReconciler) reconcileStatefulSet(ctx context.Context, m *mkov1.Mosquitto) error {
	logger := log.FromContext(ctx)

	desired, err := builder.BuildStatefulSet(m, builder.PodOptions{ReloaderImage: r.ReloaderImage})
	if err != nil {
		return err
	}

	if err := controllerutil.SetControllerReference(m, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on StatefulSet %s: %w", desired.Name, err)
	}

	current := &appsv1.StatefulSet{}
	err = r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		logger.Info("Creating StatefulSet", "name", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if err := ensureOwned(current, m, "StatefulSet"); err != nil {
		return err
	}

	if !builder.StatefulSetHasChanged(desired, current) {
		return nil
	}

	logger.Info("Updating StatefulSet", "name", desired.Name)
	builder.MergeStatefulSet(current, desired)
	return r.Update(ctx, current)
}

// updateStatus recomputes phase, readyReplicas and the Ready condition from the
// live StatefulSet and writes them if anything changed.
func (r *MosquittoReconciler) updateStatus(ctx context.Context, m *mkov1.Mosquitto) error {
	sts := &appsv1.StatefulSet{}
	key := types.NamespacedName{Name: common.StatefulSetName(m), Namespace: m.Namespace}

	if err := r.Get(ctx, key, sts); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		m.Status.ReadyReplicas = 0
		r.setPhase(m, mkov1.PhasePending, metav1.ConditionFalse,
			"StatefulSetNotFound", "Waiting for the StatefulSet to be created")
		return r.persistStatus(ctx, m)
	}

	ready := sts.Status.ReadyReplicas
	m.Status.ReadyReplicas = ready

	switch {
	case m.Spec.Replicas > 0 && ready >= m.Spec.Replicas:
		r.setPhase(m, mkov1.PhaseReady, metav1.ConditionTrue, "AllReplicasReady",
			fmt.Sprintf("%d/%d broker pods are ready", ready, m.Spec.Replicas))
	case ready > 0:
		r.setPhase(m, mkov1.PhaseProgressing, metav1.ConditionFalse, "ReplicasNotReady",
			fmt.Sprintf("%d/%d broker pods are ready", ready, m.Spec.Replicas))
	default:
		r.setPhase(m, mkov1.PhasePending, metav1.ConditionFalse, "NoReplicasReady",
			fmt.Sprintf("0/%d broker pods are ready", m.Spec.Replicas))
	}

	return r.persistStatus(ctx, m)
}

// setPhase writes the phase, the observed generation and the Ready condition in
// one place, so the two can never disagree about which generation they describe.
func (r *MosquittoReconciler) setPhase(m *mkov1.Mosquitto, phase string,
	ready metav1.ConditionStatus, reason, message string) {
	m.Status.Phase = phase
	m.Status.ObservedGeneration = m.Generation

	meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
		Type:               mkov1.ConditionTypeReady,
		Status:             ready,
		ObservedGeneration: m.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// persistStatus writes the status subresource, unless nothing about it changed.
//
// The comparison is not an optimisation: the CR watch filters out status-only
// writes, but every write still costs an API request and a resourceVersion bump
// that every informer in the cluster sees.
func (r *MosquittoReconciler) persistStatus(ctx context.Context, m *mkov1.Mosquitto) error {
	stored := &mkov1.Mosquitto{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(m), stored); err != nil {
		return err
	}
	if statusUnchanged(&stored.Status, &m.Status) {
		return nil
	}

	stored.Status = m.Status
	return r.Status().Update(ctx, stored)
}

// statusUnchanged reports whether two statuses agree on every field the operator
// writes.
func statusUnchanged(prev, curr *mkov1.MosquittoStatus) bool {
	return prev.Phase == curr.Phase &&
		prev.ReadyReplicas == curr.ReadyReplicas &&
		prev.ObservedGeneration == curr.ObservedGeneration &&
		prev.Users == curr.Users &&
		reflect.DeepEqual(prev.Conditions, curr.Conditions)
}

// setUsersCondition records how many users the broker accepts, and says so when
// it accepts nobody (ADR 0008 D13). It never touches Ready (ADR 0012 D6).
func setUsersCondition(m *mkov1.Mosquitto, accepted int) {
	m.Status.Users = int32(accepted) // #nosec G115 -- bounded by the users of one namespace
	condition := metav1.Condition{
		Type:               mkov1.ConditionTypeUsers,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: m.Generation,
		Reason:             mkov1.ReasonUsersAccepted,
		Message:            fmt.Sprintf("%d users accepted", accepted),
	}
	if accepted == 0 {
		condition.Status = metav1.ConditionFalse
		condition.Reason = mkov1.ReasonNoUsers
		condition.Message = "the broker requires a login and accepts nobody: no MosquittoUser bound to it is accepted"
	}
	meta.SetStatusCondition(&m.Status.Conditions, condition)
}

// ensureOwned refuses to write an object this Mosquitto does not control.
//
// Every managed name is derived from the resource name, so a pre-existing object
// can hold one. Adopting it would hand somebody else's workload or traffic to
// this operator, and the refusal is reported as a reconcile failure because the
// resource cannot do its job without the object.
func ensureOwned(obj metav1.Object, m *mkov1.Mosquitto, kind string) error {
	if metav1.IsControlledBy(obj, m) {
		return nil
	}
	return fmt.Errorf("%s %s/%s exists and is not owned by this Mosquitto", kind, obj.GetNamespace(), obj.GetName())
}
