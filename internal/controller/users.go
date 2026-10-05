package controller

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/auth"
	"github.com/guided-traffic/mosquitto-operator/internal/builder"
	"github.com/guided-traffic/mosquitto-operator/internal/common"
)

// userPass is what one pass found out about the users of one broker.
type userPass struct {
	users []mkov1.MosquittoUser
	// usernames holds the username read for each user whose Secret could be
	// read, by object name; it goes into status.username.
	usernames map[string]string
	verdicts  map[string]auth.Verdict
	accepted  int
	data      map[string][]byte
}

// random returns the source of password salts.
func (r *MosquittoReconciler) random() io.Reader {
	if r.Random != nil {
		return r.Random
	}
	return rand.Reader
}

// listUsers returns the MosquittoUser objects of a namespace that name a broker.
func (r *MosquittoReconciler) listUsers(ctx context.Context, namespace, broker string) ([]mkov1.MosquittoUser, error) {
	list := &mkov1.MosquittoUserList{}
	if err := r.List(ctx, list, client.InNamespace(namespace), client.MatchingFields{userBrokerField: broker}); err != nil {
		return nil, fmt.Errorf("listing the MosquittoUsers of %s/%s: %w", namespace, broker, err)
	}
	return list.Items, nil
}

// renderUsers reads the credentials of every user bound to the broker and
// renders them (ADR 0013, ADR 0014 D2, D3). A user whose credentials cannot be
// read gets its verdict here and is left out; the rest go to the renderer.
func (r *MosquittoReconciler) renderUsers(ctx context.Context, m *mkov1.Mosquitto) (userPass, error) {
	users, err := r.listUsers(ctx, m.Namespace, m.Name)
	if err != nil {
		return userPass{}, err
	}
	pass := userPass{users: users, usernames: map[string]string{}, verdicts: map[string]auth.Verdict{}}

	var inputs []auth.Input
	for i := range users {
		in, refusal, err := r.readCredentials(ctx, &users[i])
		if err != nil {
			return userPass{}, err
		}
		if refusal != nil {
			pass.verdicts[users[i].Name] = *refusal
			continue
		}
		pass.usernames[users[i].Name] = in.Username
		inputs = append(inputs, in)
	}

	previous, err := r.currentAuthData(ctx, m)
	if err != nil {
		return userPass{}, err
	}
	result, err := auth.Render(auth.FilePayload{}, inputs, previous, r.random())
	if err != nil {
		return userPass{}, err
	}
	for name, verdict := range result.Verdicts {
		pass.verdicts[name] = verdict
	}
	pass.accepted = result.Accepted
	pass.data = result.Data
	return pass, nil
}

// readCredentials reads a user's username and password. The Secret's existence
// and labels come from the cache, which holds Secret metadata only; the data is
// fetched with one uncached get, and only after --secret-security's label check
// passed, so the operator never reads the data of a Secret whose owner did not
// consent. It returns the renderer's input, or the verdict that refuses the
// user before rendering.
func (r *MosquittoReconciler) readCredentials(ctx context.Context, u *mkov1.MosquittoUser) (auth.Input, *auth.Verdict, error) {
	name := u.Spec.CredentialsSecret.Name
	key := types.NamespacedName{Namespace: u.Namespace, Name: name}
	notFound := &auth.Verdict{Reason: mkov1.ReasonSecretNotFound,
		Message: fmt.Sprintf("Secret %s does not exist in namespace %s", name, u.Namespace)}

	metadata := &corev1.Secret{}
	if err := r.Get(ctx, key, metadata); err != nil {
		if apierrors.IsNotFound(err) {
			return auth.Input{}, notFound, nil
		}
		return auth.Input{}, nil, fmt.Errorf("reading Secret %s of MosquittoUser %s: %w", name, u.Name, err)
	}
	if r.SecretSecurity && metadata.Labels[mkov1.ConsumableLabel] != mkov1.ConsumableLabelValue {
		return auth.Input{}, &auth.Verdict{Reason: mkov1.ReasonSecretNotConsumable,
			Message: fmt.Sprintf("Secret %s does not carry %s=%s, which --secret-security=true requires",
				name, mkov1.ConsumableLabel, mkov1.ConsumableLabelValue)}, nil
	}

	secret := &corev1.Secret{}
	if err := r.reader().Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return auth.Input{}, notFound, nil
		}
		return auth.Input{}, nil, fmt.Errorf("reading Secret %s of MosquittoUser %s: %w", name, u.Name, err)
	}
	username, hasUsername := secret.Data[u.UsernameKey()]
	password, hasPassword := secret.Data[u.PasswordKey()]
	for _, missing := range []struct {
		key     string
		present bool
	}{{u.UsernameKey(), hasUsername}, {u.PasswordKey(), hasPassword}} {
		if !missing.present {
			return auth.Input{}, &auth.Verdict{Reason: mkov1.ReasonKeyNotFound,
				Message: fmt.Sprintf("Secret %s has no key %q", name, missing.key)}, nil
		}
	}
	return auth.Input{
		Name:              u.Name,
		CreationTimestamp: u.CreationTimestamp.Time,
		Username:          string(username),
		Password:          string(password),
		ACLs:              u.Spec.ACLs,
	}, nil, nil
}

// currentAuthData returns the data of the broker's rendered Secret, read
// uncached, so the renderer can keep the hashes whose passwords still verify. A
// Secret this Mosquitto does not control is not trusted for that.
func (r *MosquittoReconciler) currentAuthData(ctx context.Context, m *mkov1.Mosquitto) (map[string][]byte, error) {
	current := &corev1.Secret{}
	err := r.reader().Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: common.AuthSecretName(m)}, current)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading Secret %s: %w", common.AuthSecretName(m), err)
	}
	if !metav1.IsControlledBy(current, m) {
		return nil, nil
	}
	return current.Data, nil
}

// reconcileAuthSecret ensures <name>-auth exists, is controlled by this
// Mosquitto and carries the rendered data, and writes it only on a difference,
// so an unchanged pass changes nothing the reloader would notice.
func (r *MosquittoReconciler) reconcileAuthSecret(ctx context.Context, m *mkov1.Mosquitto, data map[string][]byte) error {
	logger := log.FromContext(ctx)
	desired := builder.BuildAuthSecret(m, data)
	if err := controllerutil.SetControllerReference(m, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on Secret %s: %w", desired.Name, err)
	}

	current := &corev1.Secret{}
	err := r.reader().Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		logger.Info("Creating Secret", "name", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if err := ensureOwned(current, m, "Secret"); err != nil {
		return err
	}

	if equality.Semantic.DeepEqual(current.Data, desired.Data) &&
		!common.MapEntriesMissing(desired.Labels, current.Labels) {
		return nil
	}

	logger.Info("Updating Secret", "name", desired.Name)
	current.Data = desired.Data
	current.Labels = common.MergeLabels(current.Labels, desired.Labels)
	return r.Update(ctx, current)
}

// writeUserStatuses writes each user's verdict into its status.
func (r *MosquittoReconciler) writeUserStatuses(ctx context.Context, pass userPass) error {
	for i := range pass.users {
		u := &pass.users[i]
		if err := r.persistUserStatus(ctx, u, pass.usernames[u.Name], pass.verdicts[u.Name]); err != nil {
			return err
		}
	}
	return nil
}

// reportUsers writes one verdict into the status of every user that names the
// broker - a broker that does not exist, or one whose namespace the operator
// holds no grant for.
func (r *MosquittoReconciler) reportUsers(ctx context.Context, namespace, broker string, verdict auth.Verdict) error {
	users, err := r.listUsers(ctx, namespace, broker)
	if err != nil {
		return err
	}
	for i := range users {
		if err := r.persistUserStatus(ctx, &users[i], users[i].Status.Username, verdict); err != nil {
			return err
		}
	}
	return nil
}

// reportUsersOfMissingBroker marks every user that names a Mosquitto which does
// not exist (ADR 0013 D10).
func (r *MosquittoReconciler) reportUsersOfMissingBroker(ctx context.Context, key types.NamespacedName) error {
	return r.reportUsers(ctx, key.Namespace, key.Name, auth.Verdict{
		Reason:  mkov1.ReasonBrokerNotFound,
		Message: fmt.Sprintf("Mosquitto %s does not exist in namespace %s", key.Name, key.Namespace),
	})
}

// persistUserStatus writes a user's observedGeneration, username and Ready
// condition, unless nothing about them changed.
func (r *MosquittoReconciler) persistUserStatus(ctx context.Context, u *mkov1.MosquittoUser, username string, verdict auth.Verdict) error {
	status := *u.Status.DeepCopy()
	status.ObservedGeneration = u.Generation
	status.Username = username
	condition := metav1.Condition{
		Type:               mkov1.ConditionTypeReady,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: u.Generation,
		Reason:             verdict.Reason,
		Message:            verdict.Message,
	}
	if verdict.Accepted {
		condition.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&status.Conditions, condition)
	if reflect.DeepEqual(status, u.Status) {
		return nil
	}

	stored := u.DeepCopy()
	stored.Status = status
	if err := r.Status().Update(ctx, stored); err != nil {
		return fmt.Errorf("writing the status of MosquittoUser %s: %w", u.Name, err)
	}
	return nil
}
