package builder

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/common"
)

// brokerServicePort returns the single Service port both Services expose. The
// target is the named container port, so switching the listener between 1883 and
// 8883 needs no second lookup here.
func brokerServicePort(m *mkov1.Mosquitto) corev1.ServicePort {
	return corev1.ServicePort{
		Name:       BrokerPortName(m),
		Port:       BrokerPort(m),
		TargetPort: intstr.FromString(BrokerPortName(m)),
		Protocol:   corev1.ProtocolTCP,
	}
}

// BuildHeadlessService builds the headless Service that gives the StatefulSet its
// stable per-pod DNS records.
//
// PublishNotReadyAddresses is on: the records must resolve before a pod passes
// its readiness probe, otherwise a pod has no DNS name during startup and
// anything addressing a specific broker has to wait for readiness it may itself
// be part of.
func BuildHeadlessService(m *mkov1.Mosquitto) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.HeadlessServiceName(m),
			Namespace: m.Namespace,
			Labels:    common.BaseLabels(m, ResolveImage(m)),
		},
		Spec: corev1.ServiceSpec{
			Type:                     corev1.ServiceTypeClusterIP,
			ClusterIP:                corev1.ClusterIPNone,
			Selector:                 common.SelectorLabels(m),
			PublishNotReadyAddresses: true,
			Ports:                    []corev1.ServicePort{brokerServicePort(m)},
		},
	}
}

// BuildClientService builds the ClusterIP Service clients connect to. It
// load-balances over every ready broker pod.
func BuildClientService(m *mkov1.Mosquitto) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.ClientServiceName(m),
			Namespace: m.Namespace,
			Labels:    common.BaseLabels(m, ResolveImage(m)),
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: common.SelectorLabels(m),
			Ports:    []corev1.ServicePort{brokerServicePort(m)},
		},
	}
}
