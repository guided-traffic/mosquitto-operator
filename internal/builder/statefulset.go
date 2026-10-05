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

	// ConfigCheckContainerName is the init container that runs the broker binary
	// in --test-config mode against the generated file before the broker starts.
	ConfigCheckContainerName = "config-check"

	// AuthInitContainerName is the init container that copies the rendered
	// credentials into the broker's directory on every start (ADR 0014 D4).
	AuthInitContainerName = "auth-init"
	// ReloaderContainerName is the sidecar that copies a change of the rendered
	// credentials in and signals the broker (ADR 0014 D4-D6).
	ReloaderContainerName = "reloader"

	// OperatorBinary is where the operator image keeps its binary; auth-init and
	// the reloader run its "reload" entry point.
	OperatorBinary = "/app/manager"

	// ConfigVolumeName is the volume carrying the generated mosquitto.conf.
	ConfigVolumeName = "config"
	// TLSVolumeName is the volume carrying the referenced TLS secret.
	TLSVolumeName = "tls"
	// DataVolumeName is the volume backing the persistence directory. It is the
	// name of the PVC template when spec.storage is set, so it must not change:
	// volumeClaimTemplates are immutable once the StatefulSet exists.
	DataVolumeName = "data"
	// ConfigCheckScratchVolumeName is a throwaway emptyDir the config-check init
	// container sees at the persistence path instead of the data volume:
	// --test-config saves the broker's empty in-memory database to
	// persistence_location when it exits, which on the real data volume replaces
	// every retained message and session with nothing
	// (docs/developer/broker-behaviour.md, M19).
	ConfigCheckScratchVolumeName = "config-check-scratch"
	// AuthSecretVolumeName mounts the rendered Secret <name>-auth whole, without
	// subPath, so the kubelet refreshes it (ADR 0014 D4).
	AuthSecretVolumeName = "auth-secret"
	// AuthVolumeName is the emptyDir the broker reads its credentials from.
	AuthVolumeName = "auth"
	// AuthSecretMountPath is where auth-init and the reloader see the Secret.
	AuthSecretMountPath = "/mosquitto/auth-secret" // #nosec G101 -- a mount path, not a credential

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

	// AnnotationDefaultContainer is kubectl's own annotation naming the
	// container kubectl logs and kubectl exec use when none is given.
	AnnotationDefaultContainer = "kubectl.kubernetes.io/default-container"

	// AnnotationAppliedPodLabels and AnnotationAppliedPodAnnotations sit on the
	// StatefulSet object, not on its pods, and list the keys of spec.podLabels and
	// spec.podAnnotations the operator last wrote into the pod template (sorted,
	// comma-separated). An update merges into the template and keeps keys other
	// writers added (ADR 0009 D9), so without this record a key removed from the
	// spec would stay on the pods forever; with it, exactly the keys that left the
	// spec are removed.
	AnnotationAppliedPodLabels = "mko.gtrfc.com/applied-pod-labels"
	// AnnotationAppliedPodAnnotations is the record of spec.podAnnotations; see
	// AnnotationAppliedPodLabels.
	AnnotationAppliedPodAnnotations = "mko.gtrfc.com/applied-pod-annotations"

	// brokerUserID is the uid/gid of the "mosquitto" user in the eclipse-mosquitto
	// image. It is set explicitly (rather than left to the image) so the pod can
	// declare runAsNonRoot, and it is the fsGroup as well: without it the mounted
	// PVC is root-owned and the broker cannot write its persistence file.
	brokerUserID int64 = 1883

	// volumeDefaultMode is 0644, the mode the API server defaults a ConfigMap or
	// Secret volume to. It is written out so the desired object matches what the
	// API server stores, which keeps the pod-spec hash stable across passes.
	volumeDefaultMode int32 = 0o644

	// authSecretMode is 0440 on the mount of <name>-auth: the files are
	// root-owned and the group is the pod's fsGroup 1883, so auth-init and the
	// reloader read them and nobody else in the pod has a reason to.
	authSecretMode int32 = 0o440
)

// PodOptions is what the broker pod needs from the operator's own
// configuration rather than from the Mosquitto.
type PodOptions struct {
	// ReloaderImage is the operator's own image, which auth-init and the
	// reloader run (ADR 0014 D6): --reloader-image.
	ReloaderImage string
}

// reloaderResources bound the two containers of the operator image in a broker
// pod. They copy two small files and poll; the requests let a namespace whose
// quota demands them admit the pod.
var reloaderResources = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("10m"),
		corev1.ResourceMemory: resource.MustParse("32Mi"),
	},
	Limits: corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("64Mi"),
	},
}

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
// It fails on an unparsable spec.storage.size: that value reaches the PVC
// template, which is immutable once created, so a wrong quantity is worth a
// visible reconcile failure rather than a silently substituted default. And it
// fails without a reloader image, which only a misconfigured operator lacks.
func BuildStatefulSet(m *mkov1.Mosquitto, opts PodOptions) (*appsv1.StatefulSet, error) {
	if opts.ReloaderImage == "" {
		return nil, fmt.Errorf("no reloader image is configured (--reloader-image)")
	}
	podSpec := buildPodSpec(m, opts)
	labels := common.BaseLabels(m, ResolveImage(m))
	replicas := m.Spec.Replicas

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.StatefulSetName(m),
			Namespace: m.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				AnnotationAppliedPodLabels:      common.JoinKeys(m.Spec.PodLabels),
				AnnotationAppliedPodAnnotations: common.JoinKeys(m.Spec.PodAnnotations),
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: common.HeadlessServiceName(m),
			Selector: &metav1.LabelSelector{
				MatchLabels: common.SelectorLabels(m),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					// The user's keys go in first and the operator's are written over
					// them, so spec.podLabels cannot detach the Services from the pods
					// and spec.podAnnotations cannot forge a hash (ADR 0012 D5).
					Labels: common.MergeLabels(m.Spec.PodLabels, labels),
					Annotations: common.MergeLabels(m.Spec.PodAnnotations, map[string]string{
						AnnotationPodSpecHash: hashOf(podSpec),
						AnnotationConfigHash:  hashOf(GenerateMosquittoConf(m)),
						// kubectl logs and exec pick the broker, not the reloader.
						AnnotationDefaultContainer: BrokerContainerName,
					}),
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
func buildPodSpec(m *mkov1.Mosquitto, opts PodOptions) corev1.PodSpec {
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
		{
			Name: AuthSecretVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  common.AuthSecretName(m),
					DefaultMode: ptr.To(authSecretMode),
				},
			},
		},
		{
			Name:         AuthVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
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

	volumes = append(volumes, corev1.Volume{
		Name:         ConfigCheckScratchVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})

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
		// The reloader signals the broker across the pod's process namespace; it
		// shares the broker's uid, so it needs no capability (ADR 0014 D4, M22).
		ShareProcessNamespace: ptr.To(true),
		InitContainers: []corev1.Container{
			buildReloadContainer(AuthInitContainerName, opts, "--once"),
			buildConfigCheckContainer(m),
		},
		Containers: []corev1.Container{
			buildBrokerContainer(m),
			buildReloaderSidecar(m, opts),
		},
		Volumes:  volumes,
		Affinity: BuildPodAntiAffinity(m),
	}
}

// containerSecurityContext is the security context of every container the
// operator renders: no privilege escalation, a read-only root filesystem and no
// capabilities. The uid, the group, runAsNonRoot and the seccomp profile come
// from the pod (ADR 0012 D4).
func containerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

// buildConfigCheckContainer constructs the init container that runs the broker
// binary of the pod's own image in --test-config mode against the generated
// file (ADR 0007 D10). A directive the image does not know - a typo in
// spec.config, or a generated directive an older image lacks - stops the pod
// here with the broker's message, file and line, instead of a crash loop.
//
// It checks directive names only, nothing a plugin decides (M8), and it never
// sees the data volume: the scratch emptyDir at the persistence path receives
// the empty database --test-config saves on exit (M19).
func buildConfigCheckContainer(m *mkov1.Mosquitto) corev1.Container {
	return corev1.Container{
		Name:    ConfigCheckContainerName,
		Image:   ResolveImage(m),
		Command: []string{"/usr/sbin/mosquitto", "-c", ConfigMountPath + "/" + ConfigKey, "--test-config"},
		VolumeMounts: []corev1.VolumeMount{
			{Name: ConfigVolumeName, MountPath: ConfigMountPath, ReadOnly: true},
			{Name: ConfigCheckScratchVolumeName, MountPath: DataMountPath},
		},
		// The broker's own requests and limits: an init container that runs
		// before every other container does not raise the pod's effective
		// request, and a namespace whose quota demands limits admits it.
		Resources:       m.Spec.Resources,
		SecurityContext: containerSecurityContext(),
	}
}

// buildReloadContainer constructs auth-init (with --once) or the reloader
// sidecar: the operator image's "reload" entry point, between the Secret mount
// and the broker's directory, with the broker container's security context.
func buildReloadContainer(name string, opts PodOptions, extraArgs ...string) corev1.Container {
	args := append([]string{"reload",
		"--source", AuthSecretMountPath,
		"--target", AuthMountPath,
	}, extraArgs...)
	return corev1.Container{
		Name:    name,
		Image:   opts.ReloaderImage,
		Command: []string{OperatorBinary},
		Args:    args,
		VolumeMounts: []corev1.VolumeMount{
			{Name: AuthSecretVolumeName, MountPath: AuthSecretMountPath, ReadOnly: true},
			{Name: AuthVolumeName, MountPath: AuthMountPath},
		},
		Resources:       reloaderResources,
		SecurityContext: containerSecurityContext(),
	}
}

// buildReloaderSidecar constructs the reloader sidecar. With TLS it also reads
// the broker's TLS mount: a SIGHUP reloads the certificate as well, so the
// sidecar signals a renewed pair and holds every signal while the mounted pair
// is invalid (ADR 0001 D10, ADR 0014 D5, M12).
func buildReloaderSidecar(m *mkov1.Mosquitto, opts PodOptions) corev1.Container {
	if !m.IsTLSEnabled() {
		return buildReloadContainer(ReloaderContainerName, opts)
	}
	c := buildReloadContainer(ReloaderContainerName, opts, "--tls-dir", TLSMountPath)
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
		Name: TLSVolumeName, MountPath: TLSMountPath, ReadOnly: true,
	})
	return c
}

// buildBrokerContainer constructs the broker container of a broker pod.
func buildBrokerContainer(m *mkov1.Mosquitto) corev1.Container {
	volumeMounts := []corev1.VolumeMount{
		{Name: ConfigVolumeName, MountPath: ConfigMountPath, ReadOnly: true},
		{Name: DataVolumeName, MountPath: DataMountPath},
		{Name: AuthVolumeName, MountPath: AuthMountPath, ReadOnly: true},
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
		Resources:       m.Spec.Resources,
		SecurityContext: containerSecurityContext(),
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
// It compares the replica count, the object labels and annotations, and the pod
// template's labels and annotations - the two hash annotations and the user's
// spec.podLabels and spec.podAnnotations among them - rather than the pod spec
// itself: the API server defaults a long list of pod fields the operator never
// sets, so a structural comparison against the stored object would report a
// difference on every pass and put the StatefulSet in a permanent update loop.
// Keys only the live object carries are ignored; a key that left the spec is
// caught through the applied-keys annotations, whose value then differs.
func StatefulSetHasChanged(desired, current *appsv1.StatefulSet) bool {
	if desired.Spec.Replicas != nil && current.Spec.Replicas != nil &&
		*desired.Spec.Replicas != *current.Spec.Replicas {
		return true
	}
	return common.MapEntriesMissing(desired.Labels, current.Labels) ||
		common.MapEntriesMissing(desired.Annotations, current.Annotations) ||
		common.MapEntriesMissing(desired.Spec.Template.Labels, current.Spec.Template.Labels) ||
		common.MapEntriesMissing(desired.Spec.Template.Annotations, current.Spec.Template.Annotations)
}

// MergeStatefulSet writes desired into current the way an update must (ADR 0009
// D9): replicas and the pod spec are replaced; the object labels and
// annotations and the pod template's labels and annotations are merged, the
// operator's keys winning and every other key kept - a label from Flux or a
// policy engine, kubectl.kubernetes.io/restartedAt. The one exception are the
// keys of spec.podLabels and spec.podAnnotations that the applied-keys
// annotations of current list and those of desired no longer do: they are
// removed, so a key deleted from the spec leaves the pods.
// volumeClaimTemplates are not touched; they are immutable.
func MergeStatefulSet(current, desired *appsv1.StatefulSet) {
	template := *desired.Spec.Template.DeepCopy()
	template.Labels = common.MergeLabels(
		withoutKeys(current.Spec.Template.Labels, common.RemovedKeys(
			current.Annotations[AnnotationAppliedPodLabels], desired.Annotations[AnnotationAppliedPodLabels])),
		template.Labels)
	template.Annotations = common.MergeLabels(
		withoutKeys(current.Spec.Template.Annotations, common.RemovedKeys(
			current.Annotations[AnnotationAppliedPodAnnotations], desired.Annotations[AnnotationAppliedPodAnnotations])),
		template.Annotations)

	current.Labels = common.MergeLabels(current.Labels, desired.Labels)
	current.Annotations = common.MergeLabels(current.Annotations, desired.Annotations)
	current.Spec.Replicas = desired.Spec.Replicas
	current.Spec.Template = template
}

// withoutKeys returns a copy of m without the given keys.
func withoutKeys(m map[string]string, keys []string) map[string]string {
	out := common.MergeLabels(m, nil)
	for _, k := range keys {
		delete(out, k)
	}
	return out
}
