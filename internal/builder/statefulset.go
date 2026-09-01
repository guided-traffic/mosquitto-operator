package builder

import (
	"encoding/json"
	"fmt"
	"hash/fnv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/common"
)

const (
	// DefaultImage is the broker image used when spec.image is empty.
	// renovate: datasource=docker depName=eclipse-mosquitto
	DefaultImage = "eclipse-mosquitto:2.1.2-alpine"

	// BrokerContainerName is the name of the broker container.
	BrokerContainerName = "mosquitto"

	// ConfigVolumeName is the volume carrying the generated mosquitto.conf.
	ConfigVolumeName = "config"
	// TLSVolumeName is the volume carrying the referenced TLS secret.
	TLSVolumeName = "tls"
	// DataVolumeName is the volume backing the persistence directory. It is the
	// name of the PVC template when spec.storage is set, so it must not change:
	// volumeClaimTemplates are immutable once the StatefulSet exists.
	DataVolumeName = "data"

	// AnnotationPodSpecHash carries a digest of the pod spec the operator built.
	// The StatefulSet controller rolls the pods when the template changes, and
	// this annotation is what makes a change it would otherwise not notice — one
	// that only affects a field the operator computes — part of that template.
	AnnotationPodSpecHash = "mko.gtrfc.com/pod-spec-hash"

	// AnnotationConfigHash carries a digest of the generated mosquitto.conf.
	// Mosquitto reads its configuration once at startup and a ConfigMap update
	// does not restart anything, so without this annotation a config change would
	// sit in the ConfigMap and never reach a running broker.
	AnnotationConfigHash = "mko.gtrfc.com/config-hash"

	// brokerUserID is the uid/gid of the "mosquitto" user in the eclipse-mosquitto
	// image. It is set explicitly (rather than left to the image) so the pod can
	// declare runAsNonRoot, and it is the fsGroup as well: without it the mounted
	// PVC is root-owned and the broker cannot write its persistence file.
	brokerUserID int64 = 1883

	// volumeDefaultMode is 0644, the mode the API server defaults a ConfigMap or
	// Secret volume to. It is written out so the desired object matches what the
	// API server stores, which keeps the pod-spec hash stable across passes.
	volumeDefaultMode int32 = 0o644
)

// ResolveImage returns the broker image the pods run: the spec value, or the
// pinned default when the spec leaves it empty.
func ResolveImage(m *mkov1.Mosquitto) string {
	if m.Spec.Image != "" {
		return m.Spec.Image
	}
	return DefaultImage
}

// BuildStatefulSet builds the broker StatefulSet.
//
// It fails only on an unparsable spec.storage.size: that value reaches the PVC
// template, which is immutable once created, so a wrong quantity is worth a
// visible reconcile failure rather than a silently substituted default.
func BuildStatefulSet(m *mkov1.Mosquitto) (*appsv1.StatefulSet, error) {
	podSpec := buildPodSpec(m)
	labels := common.BaseLabels(m, ResolveImage(m))
	replicas := m.Spec.Replicas

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.StatefulSetName(m),
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: common.HeadlessServiceName(m),
			Selector: &metav1.LabelSelector{
				MatchLabels: common.SelectorLabels(m),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						AnnotationPodSpecHash: hashOf(podSpec),
						AnnotationConfigHash:  hashOf(GenerateMosquittoConf(m)),
					},
				},
				Spec: podSpec,
			},
		},
	}

	if m.IsStorageEnabled() {
		templates, err := buildVolumeClaimTemplates(m)
		if err != nil {
			return nil, err
		}
		sts.Spec.VolumeClaimTemplates = templates
	}

	return sts, nil
}

// buildPodSpec constructs the PodSpec of a broker pod.
func buildPodSpec(m *mkov1.Mosquitto) corev1.PodSpec {
	volumes := []corev1.Volume{
		{
			Name: ConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: ConfigMapName(m)},
					DefaultMode:          ptr.To(volumeDefaultMode),
				},
			},
		},
	}

	if m.IsTLSEnabled() {
		volumes = append(volumes, corev1.Volume{
			Name: TLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  m.Spec.TLS.SecretName,
					DefaultMode: ptr.To(volumeDefaultMode),
				},
			},
		})
	}

	// Without a PVC template the persistence directory is still a mount, just an
	// ephemeral one. Keeping the mount unconditional means the generated
	// configuration writes to the same path in both cases.
	if !m.IsStorageEnabled() {
		volumes = append(volumes, corev1.Volume{
			Name:         DataVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	}

	return corev1.PodSpec{
		// The operator issues no API calls from the broker pod, so it takes the
		// ServiceAccount token away rather than leaving the default one mounted.
		AutomountServiceAccountToken: ptr.To(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: ptr.To(true),
			RunAsUser:    ptr.To(brokerUserID),
			RunAsGroup:   ptr.To(brokerUserID),
			FSGroup:      ptr.To(brokerUserID),
			// The restricted Pod Security Standard requires a seccompProfile;
			// without one every broker pod is rejected outright in a namespace
			// labelled pod-security.kubernetes.io/enforce=restricted. It is set at
			// pod level rather than on the container so a container added later
			// inherits it instead of needing its own copy.
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{buildBrokerContainer(m)},
		Volumes:    volumes,
		Affinity:   BuildPodAntiAffinity(m),
	}
}

// buildBrokerContainer constructs the single container of a broker pod.
func buildBrokerContainer(m *mkov1.Mosquitto) corev1.Container {
	volumeMounts := []corev1.VolumeMount{
		{Name: ConfigVolumeName, MountPath: ConfigMountPath, ReadOnly: true},
		{Name: DataVolumeName, MountPath: DataMountPath},
	}
	if m.IsTLSEnabled() {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name: TLSVolumeName, MountPath: TLSMountPath, ReadOnly: true,
		})
	}

	port := BrokerPort(m)
	probeHandler := corev1.ProbeHandler{
		TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
	}

	return corev1.Container{
		Name:  BrokerContainerName,
		Image: ResolveImage(m),
		// The image's entrypoint chowns /mosquitto when it runs as root; this pod
		// never does, so the broker is started directly and the configuration path
		// is named rather than inherited from the image's CMD.
		Command: []string{"/usr/sbin/mosquitto", "-c", ConfigMountPath + "/" + ConfigKey},
		Ports: []corev1.ContainerPort{{
			Name:          BrokerPortName(m),
			ContainerPort: port,
			Protocol:      corev1.ProtocolTCP,
		}},
		VolumeMounts: volumeMounts,
		// A TCP probe is the whole readiness statement the operator can make
		// without speaking MQTT: the listener accepts connections. It covers both
		// the plain and the TLS listener, because a TLS handshake starts with the
		// same accept.
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:        probeHandler,
			InitialDelaySeconds: 5,
			PeriodSeconds:       5,
			TimeoutSeconds:      3,
			SuccessThreshold:    1,
			FailureThreshold:    3,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler:        probeHandler,
			InitialDelaySeconds: 15,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			SuccessThreshold:    1,
			FailureThreshold:    5,
		},
		Resources: m.Spec.Resources,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}

// buildVolumeClaimTemplates creates the PVC template for the persistence
// directory.
//
// The template is written on creation and never updated: volumeClaimTemplates
// are immutable, so a later change to spec.storage does not converge and the
// StatefulSet has to be recreated by hand.
func buildVolumeClaimTemplates(m *mkov1.Mosquitto) ([]corev1.PersistentVolumeClaim, error) {
	size, err := resource.ParseQuantity(m.Spec.Storage.Size)
	if err != nil {
		return nil, fmt.Errorf("parsing spec.storage.size %q: %w", m.Spec.Storage.Size, err)
	}

	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   DataVolumeName,
			Labels: common.BaseLabels(m, ResolveImage(m)),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: m.Spec.Storage.StorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: size},
			},
		},
	}

	return []corev1.PersistentVolumeClaim{pvc}, nil
}

// hashOf returns a short hex digest of the JSON encoding of v. It is only used
// to detect change, never to prove identity, so a 32-bit non-cryptographic hash
// is enough.
func hashOf(v any) string {
	data, _ := json.Marshal(v)
	h := fnv.New32a()
	_, _ = h.Write(data)
	return fmt.Sprintf("%08x", h.Sum32())
}

// StatefulSetHasChanged reports whether the live StatefulSet has to be updated to
// match the desired one.
//
// It compares the replica count, the object labels and the two hash annotations
// on the pod template rather than the pod spec itself: the API server defaults a
// long list of pod fields the operator never sets, so a structural comparison
// against the stored object would report a difference on every pass and put the
// StatefulSet in a permanent update loop.
func StatefulSetHasChanged(desired, current *appsv1.StatefulSet) bool {
	if desired.Spec.Replicas != nil && current.Spec.Replicas != nil &&
		*desired.Spec.Replicas != *current.Spec.Replicas {
		return true
	}
	if common.MapEntriesMissing(desired.Labels, current.Labels) {
		return true
	}
	if common.MapEntriesMissing(desired.Spec.Template.Labels, current.Spec.Template.Labels) {
		return true
	}
	for _, key := range []string{AnnotationPodSpecHash, AnnotationConfigHash} {
		if desired.Spec.Template.Annotations[key] != current.Spec.Template.Annotations[key] {
			return true
		}
	}
	return false
}
