package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlbuilder "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
)

// The field indexes the watches look objects up by.
const (
	// userBrokerField indexes a MosquittoUser by the broker it names.
	userBrokerField = "spec.brokerRef.name"
	// userSecretField indexes a MosquittoUser by its credentials Secret.
	userSecretField = "spec.credentialsSecret.name"
	// tlsSecretField indexes a Mosquitto by its TLS Secret.
	tlsSecretField = "spec.tls.secretName" // #nosec G101 -- a field path, not a credential
)

// SetupWithManager registers the indexes, the watches and the controller.
//
// GenerationChangedPredicate keeps the operator's own status writes - on the
// Mosquitto and on its users - from waking it again. Changes to the owned
// objects arrive through the Owns watches, which is how a StatefulSet's
// readiness reaches status and how a hand edit of <name>-auth is undone. A user
// wakes the broker it names, and on a change of brokerRef the broker it named
// before as well; a Secret wakes the brokers whose users or whose TLS listener
// name it (ADR 0012 D6: what a resource references arriving later converges
// without a manual step).
func (r *MosquittoReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := indexFields(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(ctrlcontroller.Options{
			MaxConcurrentReconciles: maxConcurrentReconciles(r.MaxConcurrentReconciles),
		}).
		For(&mkov1.Mosquitto{}, ctrlbuilder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Watches(&mkov1.MosquittoUser{}, userBrokers(),
			ctrlbuilder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.brokersForSecret)).
		Complete(r)
}

// indexFields registers the three field indexes.
func indexFields(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &mkov1.MosquittoUser{}, userBrokerField, indexUserBroker); err != nil {
		return err
	}
	if err := indexer.IndexField(ctx, &mkov1.MosquittoUser{}, userSecretField, indexUserSecret); err != nil {
		return err
	}
	return indexer.IndexField(ctx, &mkov1.Mosquitto{}, tlsSecretField, indexTLSSecret)
}

func indexUserBroker(o client.Object) []string {
	return []string{o.(*mkov1.MosquittoUser).Spec.BrokerRef.Name}
}

func indexUserSecret(o client.Object) []string {
	return []string{o.(*mkov1.MosquittoUser).Spec.CredentialsSecret.Name}
}

func indexTLSSecret(o client.Object) []string {
	m := o.(*mkov1.Mosquitto)
	if !m.IsTLSEnabled() {
		return nil
	}
	return []string{m.Spec.TLS.SecretName}
}

// userBrokers enqueues the broker a MosquittoUser names, and on an update the
// broker it named before too, so a user that moves leaves its old broker's
// credentials in the same breath as it enters the new one's.
func userBrokers() handler.TypedEventHandler[client.Object, reconcile.Request] {
	enqueue := func(q workqueue.TypedRateLimitingInterface[reconcile.Request], obj client.Object) {
		if u, ok := obj.(*mkov1.MosquittoUser); ok {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: u.Namespace, Name: u.Spec.BrokerRef.Name,
			}})
		}
	}
	return handler.TypedFuncs[client.Object, reconcile.Request]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(q, e.Object)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(q, e.ObjectOld)
			enqueue(q, e.ObjectNew)
		},
		DeleteFunc: func(_ context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(q, e.Object)
		},
		GenericFunc: func(_ context.Context, e event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(q, e.Object)
		},
	}
}

// brokersForSecret maps a Secret to the brokers whose users name it as their
// credentials, and to the brokers that name it as their TLS Secret.
func (r *MosquittoReconciler) brokersForSecret(ctx context.Context, secret client.Object) []reconcile.Request {
	seen := map[types.NamespacedName]bool{}
	var requests []reconcile.Request
	add := func(name string) {
		key := types.NamespacedName{Namespace: secret.GetNamespace(), Name: name}
		if !seen[key] {
			seen[key] = true
			requests = append(requests, reconcile.Request{NamespacedName: key})
		}
	}

	users := &mkov1.MosquittoUserList{}
	if err := r.List(ctx, users, client.InNamespace(secret.GetNamespace()),
		client.MatchingFields{userSecretField: secret.GetName()}); err == nil {
		for i := range users.Items {
			add(users.Items[i].Spec.BrokerRef.Name)
		}
	}
	brokers := &mkov1.MosquittoList{}
	if err := r.List(ctx, brokers, client.InNamespace(secret.GetNamespace()),
		client.MatchingFields{tlsSecretField: secret.GetName()}); err == nil {
		for i := range brokers.Items {
			add(brokers.Items[i].Name)
		}
	}
	return requests
}

// StripSecret is the cache transform for Secrets: it keeps what the watches and
// the ownership check need - name, namespace, labels, owner references, type,
// resource version - and drops the data, the string data, the annotations (a
// kubectl apply records the whole Secret in one of them) and the managed
// fields. The cache then holds no credential of the cluster; a pass that needs
// a Secret's data reads it with an uncached get (MosquittoReconciler.APIReader).
func StripSecret(obj any) (any, error) {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return obj, nil
	}
	secret.Data = nil
	secret.StringData = nil
	secret.Annotations = nil
	secret.ManagedFields = nil
	return secret, nil
}

// maxConcurrentReconciles resolves the configured worker count. A reconciler
// built without the field - every test, and any caller that forgets it - would
// otherwise inherit controller-runtime's single worker.
func maxConcurrentReconciles(configured int) int {
	if configured <= 0 {
		return DefaultMaxConcurrentReconciles
	}
	return configured
}
