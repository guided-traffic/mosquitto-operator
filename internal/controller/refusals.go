package controller

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/auth"
	"github.com/guided-traffic/mosquitto-operator/internal/builder"
)

// refusePass applies the checks that refuse a whole pass before anything is
// written, in order: the Secret grant of --secret-namespaces, the spec.config
// allowlist, and --secret-security on the TLS Secret. A refusal sets the
// resource Failed with a reason naming what is wrong, and the running broker
// keeps what it has. It reports whether it refused.
func (r *MosquittoReconciler) refusePass(ctx context.Context, m *mkov1.Mosquitto) (bool, error) {
	if !r.namespaceGranted(m.Namespace) {
		message := fmt.Sprintf("the operator runs with --secret-namespaces=%v, which does not grant it the Secrets of namespace %s",
			r.SecretNamespaces, m.Namespace)
		r.setPhase(m, mkov1.PhaseFailed, metav1.ConditionFalse, mkov1.ReasonNamespaceNotGranted, message)
		return true, r.reportUsers(ctx, m.Namespace, m.Name, auth.Verdict{
			Reason: mkov1.ReasonNamespaceNotGranted, Message: message,
		})
	}
	if err := builder.ValidateSpecConfig(m); err != nil {
		r.setPhase(m, mkov1.PhaseFailed, metav1.ConditionFalse, mkov1.ReasonConfigDirectiveRefused,
			err.Error()+"; nothing was written, the broker keeps its running configuration")
		return true, nil
	}
	return r.refuseTLSSecret(ctx, m)
}

// namespaceGranted reports whether the operator holds the Secret grant in a
// namespace: always without --secret-namespaces, otherwise when it is listed.
func (r *MosquittoReconciler) namespaceGranted(namespace string) bool {
	return len(r.SecretNamespaces) == 0 || slices.Contains(r.SecretNamespaces, namespace)
}

// reader returns the uncached reader, or the client when none is set.
func (r *MosquittoReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// refuseTLSSecret applies --secret-security to the TLS Secret a Mosquitto names
// (ADR 0014 D10). It reads the Secret's metadata only - never its data - and
// refuses one that is missing or does not carry the consent label: the Ready
// condition says why, and nothing is written, so a running StatefulSet stays as
// it is. A label added later wakes the broker through the Secret watch. It
// reports whether it refused.
func (r *MosquittoReconciler) refuseTLSSecret(ctx context.Context, m *mkov1.Mosquitto) (bool, error) {
	if !r.SecretSecurity || !m.IsTLSEnabled() {
		return false, nil
	}

	secret := &metav1.PartialObjectMetadata{}
	secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	err := r.reader().Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: m.Spec.TLS.SecretName}, secret)

	switch {
	case apierrors.IsNotFound(err):
		r.setPhase(m, mkov1.PhaseFailed, metav1.ConditionFalse, mkov1.ReasonSecretNotFound,
			fmt.Sprintf("TLS Secret %s does not exist; with --secret-security=true it must exist and carry %s=%s",
				m.Spec.TLS.SecretName, mkov1.ConsumableLabel, mkov1.ConsumableLabelValue))
		return true, nil
	case err != nil:
		return false, fmt.Errorf("reading the metadata of TLS Secret %s: %w", m.Spec.TLS.SecretName, err)
	case secret.GetLabels()[mkov1.ConsumableLabel] != mkov1.ConsumableLabelValue:
		r.setPhase(m, mkov1.PhaseFailed, metav1.ConditionFalse, mkov1.ReasonSecretNotConsumable,
			fmt.Sprintf("TLS Secret %s does not carry %s=%s, which --secret-security=true requires",
				m.Spec.TLS.SecretName, mkov1.ConsumableLabel, mkov1.ConsumableLabelValue))
		return true, nil
	}
	return false, nil
}
