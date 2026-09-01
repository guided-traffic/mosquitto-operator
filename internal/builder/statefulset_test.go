package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/common"
)

// mustBuild builds the StatefulSet and fails the test if the spec is unbuildable.
func mustBuild(t *testing.T, m *mkov1.Mosquitto) *appsv1.StatefulSet {
	t.Helper()
	sts, err := BuildStatefulSet(m)
	require.NoError(t, err)
	return sts
}

// containerVolumeMount returns the mount with the given name, or nil.
func containerVolumeMount(c corev1.Container, name string) *corev1.VolumeMount {
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Name == name {
			return &c.VolumeMounts[i]
		}
	}
	return nil
}

// podVolume returns the volume with the given name, or nil.
func podVolume(spec corev1.PodSpec, name string) *corev1.Volume {
	for i := range spec.Volumes {
		if spec.Volumes[i].Name == name {
			return &spec.Volumes[i]
		}
	}
	return nil
}

func TestResolveImage(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  string
	}{
		{"empty spec.image uses the pinned default", "", DefaultImage},
		{"spec.image wins", "eclipse-mosquitto:2.1.0", "eclipse-mosquitto:2.1.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Image = tt.image })
			assert.Equal(t, tt.want, ResolveImage(m))
			assert.Equal(t, tt.want, mustBuild(t, m).Spec.Template.Spec.Containers[0].Image)
		})
	}
}

func TestBuildStatefulSet_Skeleton(t *testing.T) {
	m := newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Replicas = 3 })
	sts := mustBuild(t, m)

	assert.Equal(t, "broker", sts.Name)
	assert.Equal(t, "messaging", sts.Namespace)
	assert.Equal(t, "broker-headless", sts.Spec.ServiceName,
		"the StatefulSet must name the headless Service or its pods get no DNS records")
	require.NotNil(t, sts.Spec.Replicas)
	assert.Equal(t, int32(3), *sts.Spec.Replicas)
	assert.Equal(t, common.SelectorLabels(m), sts.Spec.Selector.MatchLabels)
	assert.Equal(t, common.BaseLabels(m, DefaultImage), sts.Labels)

	for k, v := range sts.Spec.Selector.MatchLabels {
		assert.Equal(t, v, sts.Spec.Template.Labels[k],
			"selector label %q must be on the pod template or the StatefulSet is rejected", k)
	}
}

// TestBuildStatefulSet_ReplicasPointerIsNotSharedWithTheSpec guards against the
// classic aliasing bug: handing out &m.Spec.Replicas would let a caller mutating
// the built object write straight into the cached custom resource.
func TestBuildStatefulSet_ReplicasPointerIsNotSharedWithTheSpec(t *testing.T) {
	m := newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Replicas = 2 })
	sts := mustBuild(t, m)

	*sts.Spec.Replicas = 7
	assert.Equal(t, int32(2), m.Spec.Replicas)
}

func TestBuildStatefulSet_Container(t *testing.T) {
	m := newMosquitto()
	container := mustBuild(t, m).Spec.Template.Spec.Containers[0]

	assert.Equal(t, BrokerContainerName, container.Name)
	assert.Equal(t, []string{"/usr/sbin/mosquitto", "-c", "/mosquitto/config/mosquitto.conf"}, container.Command)

	require.Len(t, container.Ports, 1)
	assert.Equal(t, MQTTPortName, container.Ports[0].Name)
	assert.Equal(t, MQTTPort, container.Ports[0].ContainerPort)

	require.NotNil(t, container.ReadinessProbe)
	require.NotNil(t, container.ReadinessProbe.TCPSocket)
	assert.Equal(t, MQTTPort, container.ReadinessProbe.TCPSocket.Port.IntVal)
	require.NotNil(t, container.LivenessProbe)
	assert.Equal(t, MQTTPort, container.LivenessProbe.TCPSocket.Port.IntVal)

	require.NotNil(t, container.SecurityContext)
	assert.Equal(t, ptr.To(false), container.SecurityContext.AllowPrivilegeEscalation)
	assert.Equal(t, ptr.To(true), container.SecurityContext.ReadOnlyRootFilesystem)
	assert.Equal(t, []corev1.Capability{"ALL"}, container.SecurityContext.Capabilities.Drop)
}

func TestBuildStatefulSet_PodSecurityContext(t *testing.T) {
	spec := mustBuild(t, newMosquitto()).Spec.Template.Spec

	assert.Equal(t, ptr.To(false), spec.AutomountServiceAccountToken,
		"the broker makes no API calls, so it gets no ServiceAccount token")
	require.NotNil(t, spec.SecurityContext)
	assert.Equal(t, ptr.To(true), spec.SecurityContext.RunAsNonRoot)
	assert.Equal(t, ptr.To(int64(1883)), spec.SecurityContext.RunAsUser)
	assert.Equal(t, ptr.To(int64(1883)), spec.SecurityContext.FSGroup,
		"without fsGroup the mounted volume is root-owned and the broker cannot persist")
	require.NotNil(t, spec.SecurityContext.SeccompProfile)
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, spec.SecurityContext.SeccompProfile.Type,
		"the profile is set at pod level so a container added later inherits it")
}

// restrictedVolumeTypes are the volume types the restricted Pod Security
// Standard permits. Anything else — a hostPath above all — is rejected.
var restrictedVolumeTypes = map[string]func(corev1.VolumeSource) bool{
	"configMap":             func(v corev1.VolumeSource) bool { return v.ConfigMap != nil },
	"secret":                func(v corev1.VolumeSource) bool { return v.Secret != nil },
	"emptyDir":              func(v corev1.VolumeSource) bool { return v.EmptyDir != nil },
	"persistentVolumeClaim": func(v corev1.VolumeSource) bool { return v.PersistentVolumeClaim != nil },
	"projected":             func(v corev1.VolumeSource) bool { return v.Projected != nil },
	"downwardAPI":           func(v corev1.VolumeSource) bool { return v.DownwardAPI != nil },
	"ephemeral":             func(v corev1.VolumeSource) bool { return v.Ephemeral != nil },
	"csi":                   func(v corev1.VolumeSource) bool { return v.CSI != nil },
}

// assertRestrictedPodSecurityStandard checks the pod spec against every control
// of the restricted Pod Security Standard that this operator can violate: the
// host namespaces, the volume types, and the four container-level settings plus
// the seccomp profile.
//
// It is spelled out here rather than delegated to k8s.io/pod-security-admission
// on purpose — that module is not a dependency of this operator, and adding one
// so a test can restate a list of five field values is a poor trade.
func assertRestrictedPodSecurityStandard(t *testing.T, spec corev1.PodSpec) {
	t.Helper()

	assert.False(t, spec.HostNetwork, "restricted PSS forbids hostNetwork")
	assert.False(t, spec.HostPID, "restricted PSS forbids hostPID")
	assert.False(t, spec.HostIPC, "restricted PSS forbids hostIPC")

	require.NotNil(t, spec.SecurityContext, "restricted PSS needs runAsNonRoot and a seccompProfile")
	assert.Equal(t, ptr.To(true), spec.SecurityContext.RunAsNonRoot, "restricted PSS requires runAsNonRoot")
	require.NotNil(t, spec.SecurityContext.RunAsUser, "the broker must not inherit the image's uid")
	assert.NotEqual(t, int64(0), *spec.SecurityContext.RunAsUser, "restricted PSS forbids uid 0")

	// The pod-level profile covers every container, which is why the containers
	// below are not required to carry one of their own.
	require.NotNil(t, spec.SecurityContext.SeccompProfile, "restricted PSS requires a seccompProfile")
	assert.Contains(t,
		[]corev1.SeccompProfileType{corev1.SeccompProfileTypeRuntimeDefault, corev1.SeccompProfileTypeLocalhost},
		spec.SecurityContext.SeccompProfile.Type,
		"restricted PSS accepts only RuntimeDefault or Localhost")

	for _, volume := range spec.Volumes {
		var permitted bool
		for _, isType := range restrictedVolumeTypes {
			if isType(volume.VolumeSource) {
				permitted = true
				break
			}
		}
		assert.True(t, permitted, "volume %q is not a volume type restricted PSS permits", volume.Name)
	}

	containers := append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...)
	require.NotEmpty(t, containers)
	for _, container := range containers {
		require.NotNil(t, container.SecurityContext, "container %q carries no securityContext", container.Name)
		assert.NotEqual(t, ptr.To(true), container.SecurityContext.Privileged,
			"restricted PSS forbids privileged containers")
		assert.Equal(t, ptr.To(false), container.SecurityContext.AllowPrivilegeEscalation,
			"restricted PSS requires allowPrivilegeEscalation: false")
		require.NotNil(t, container.SecurityContext.Capabilities,
			"restricted PSS requires capabilities.drop: [ALL]")
		assert.Contains(t, container.SecurityContext.Capabilities.Drop, corev1.Capability("ALL"),
			"restricted PSS requires capabilities.drop: [ALL]")
		assert.Empty(t, container.SecurityContext.Capabilities.Add,
			"restricted PSS permits only NET_BIND_SERVICE, and this broker binds an unprivileged port")
	}
}

// TestBuildStatefulSet_SatisfiesRestrictedPodSecurityStandard covers every shape
// the builder can produce, because a namespace labelled
// pod-security.kubernetes.io/enforce=restricted rejects the pod outright — the
// StatefulSet is admitted and then creates nothing, which is the hardest kind of
// failure to read from a CR that merely stays Pending.
func TestBuildStatefulSet_SatisfiesRestrictedPodSecurityStandard(t *testing.T) {
	tests := []struct {
		name     string
		mutators []func(*mkov1.Mosquitto)
	}{
		{"plain listener, ephemeral persistence", nil},
		{"TLS secret mounted", []func(*mkov1.Mosquitto){withTLS("broker-tls")}},
		{"PVC-backed persistence", []func(*mkov1.Mosquitto){withStorage("1Gi")}},
		{"TLS and storage together", []func(*mkov1.Mosquitto){withTLS("broker-tls"), withStorage("1Gi")}},
		{"hard anti-affinity", []func(*mkov1.Mosquitto){func(m *mkov1.Mosquitto) {
			m.Spec.AntiAffinity = mkov1.AntiAffinityModeHard
		}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRestrictedPodSecurityStandard(t, mustBuild(t, newMosquitto(tt.mutators...)).Spec.Template.Spec)
		})
	}
}

func TestBuildStatefulSet_Resources(t *testing.T) {
	want := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
	}
	m := newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Resources = want })

	assert.Equal(t, want, mustBuild(t, m).Spec.Template.Spec.Containers[0].Resources)
}

func TestBuildStatefulSet_TLSOff(t *testing.T) {
	sts := mustBuild(t, newMosquitto())
	spec := sts.Spec.Template.Spec

	assert.Nil(t, podVolume(spec, TLSVolumeName))
	assert.Nil(t, containerVolumeMount(spec.Containers[0], TLSVolumeName))
	assert.Equal(t, MQTTPort, spec.Containers[0].Ports[0].ContainerPort)
}

func TestBuildStatefulSet_TLSOn(t *testing.T) {
	// A secret name that is not the one every other test uses, so the assertion
	// below proves the name comes from the spec and not from a constant.
	m := newMosquitto(withTLS("issued-by-cert-manager"))
	spec := mustBuild(t, m).Spec.Template.Spec

	volume := podVolume(spec, TLSVolumeName)
	require.NotNil(t, volume)
	require.NotNil(t, volume.Secret)
	assert.Equal(t, "issued-by-cert-manager", volume.Secret.SecretName)

	mount := containerVolumeMount(spec.Containers[0], TLSVolumeName)
	require.NotNil(t, mount)
	assert.Equal(t, TLSMountPath, mount.MountPath)
	assert.True(t, mount.ReadOnly)

	assert.Equal(t, MQTTSPort, spec.Containers[0].Ports[0].ContainerPort)
	assert.Equal(t, MQTTSPortName, spec.Containers[0].Ports[0].Name)
	assert.Equal(t, MQTTSPort, spec.Containers[0].ReadinessProbe.TCPSocket.Port.IntVal)
}

func TestBuildStatefulSet_ConfigMountIsAlwaysReadOnly(t *testing.T) {
	spec := mustBuild(t, newMosquitto()).Spec.Template.Spec

	volume := podVolume(spec, ConfigVolumeName)
	require.NotNil(t, volume)
	require.NotNil(t, volume.ConfigMap)
	assert.Equal(t, "broker-config", volume.ConfigMap.Name)

	mount := containerVolumeMount(spec.Containers[0], ConfigVolumeName)
	require.NotNil(t, mount)
	assert.Equal(t, ConfigMountPath, mount.MountPath)
	assert.True(t, mount.ReadOnly)
}

// TestBuildStatefulSet_StorageOffUsesAnEmptyDir pins the pair the data mount
// forms: with no claim the path still exists, so the generated configuration
// writes to /mosquitto/data either way.
func TestBuildStatefulSet_StorageOffUsesAnEmptyDir(t *testing.T) {
	sts := mustBuild(t, newMosquitto())
	spec := sts.Spec.Template.Spec

	assert.Empty(t, sts.Spec.VolumeClaimTemplates)

	volume := podVolume(spec, DataVolumeName)
	require.NotNil(t, volume)
	assert.NotNil(t, volume.EmptyDir)

	mount := containerVolumeMount(spec.Containers[0], DataVolumeName)
	require.NotNil(t, mount)
	assert.Equal(t, DataMountPath, mount.MountPath)
	assert.False(t, mount.ReadOnly, "the broker writes its persistence file here")
}

func TestBuildStatefulSet_StorageOnUsesAClaimTemplate(t *testing.T) {
	m := newMosquitto(withStorage("5Gi"))
	sts := mustBuild(t, m)

	assert.Nil(t, podVolume(sts.Spec.Template.Spec, DataVolumeName),
		"the claim template provides the volume, a second one of the same name would collide")

	require.Len(t, sts.Spec.VolumeClaimTemplates, 1)
	pvc := sts.Spec.VolumeClaimTemplates[0]
	assert.Equal(t, DataVolumeName, pvc.Name)
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, pvc.Spec.AccessModes)
	assert.Equal(t, resource.MustParse("5Gi"), pvc.Spec.Resources.Requests[corev1.ResourceStorage])
	assert.Nil(t, pvc.Spec.StorageClassName, "an unset class must stay unset so the cluster default applies")

	mount := containerVolumeMount(sts.Spec.Template.Spec.Containers[0], DataVolumeName)
	require.NotNil(t, mount)
	assert.Equal(t, DataMountPath, mount.MountPath)
}

func TestBuildStatefulSet_StorageClassName(t *testing.T) {
	m := newMosquitto(withStorage("1Gi"))
	m.Spec.Storage.StorageClassName = ptr.To("fast-ssd")

	sts := mustBuild(t, m)
	require.Len(t, sts.Spec.VolumeClaimTemplates, 1)
	require.NotNil(t, sts.Spec.VolumeClaimTemplates[0].Spec.StorageClassName)
	assert.Equal(t, "fast-ssd", *sts.Spec.VolumeClaimTemplates[0].Spec.StorageClassName)
}

// TestBuildStatefulSet_UnparsableStorageSizeFails is why BuildStatefulSet
// returns an error at all: the value ends up in an immutable claim template, so
// substituting a default would silently give the user a volume they did not ask
// for.
func TestBuildStatefulSet_UnparsableStorageSizeFails(t *testing.T) {
	sts, err := BuildStatefulSet(newMosquitto(withStorage("5 gigabytes")))

	require.Error(t, err)
	assert.Nil(t, sts)
	assert.Contains(t, err.Error(), "spec.storage.size")
}

func TestBuildStatefulSet_AntiAffinityReachesThePodSpec(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		wantAffinity bool
	}{
		{"off", mkov1.AntiAffinityModeOff, false},
		{"soft", mkov1.AntiAffinityModeSoft, true},
		{"hard", mkov1.AntiAffinityModeHard, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := mustBuild(t, newMosquitto(withAntiAffinity(tt.mode))).Spec.Template.Spec
			if !tt.wantAffinity {
				assert.Nil(t, spec.Affinity)
				return
			}
			require.NotNil(t, spec.Affinity)
			assert.NotNil(t, spec.Affinity.PodAntiAffinity)
		})
	}
}

// TestPodTemplateHashesChangeWithTheThingTheyDigest is what makes a change the
// StatefulSet controller would otherwise not see — one that only affects a field
// the operator computes — roll the pods.
func TestPodTemplateHashesChangeWithTheThingTheyDigest(t *testing.T) {
	base := mustBuild(t, newMosquitto()).Spec.Template.Annotations

	tests := []struct {
		name            string
		mutate          func(*mkov1.Mosquitto)
		wantPodSpecHash bool
		wantConfigHash  bool
	}{
		{"same spec", func(*mkov1.Mosquitto) {}, false, false},
		{"image change", func(m *mkov1.Mosquitto) { m.Spec.Image = "eclipse-mosquitto:2.1.0" }, true, false},
		{"extra config", func(m *mkov1.Mosquitto) { m.Spec.Config = "max_inflight_messages 40" }, false, true},
		{"tls changes both the listener and the mounts", withTLS("broker-tls"), true, true},
		{"anti-affinity", withAntiAffinity(mkov1.AntiAffinityModeHard), true, false},
		{"storage swaps the emptyDir for a claim", withStorage("1Gi"), true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mustBuild(t, newMosquitto(tt.mutate)).Spec.Template.Annotations

			assert.Equal(t, tt.wantPodSpecHash, base[AnnotationPodSpecHash] != got[AnnotationPodSpecHash],
				"pod spec hash %q vs %q", base[AnnotationPodSpecHash], got[AnnotationPodSpecHash])
			assert.Equal(t, tt.wantConfigHash, base[AnnotationConfigHash] != got[AnnotationConfigHash],
				"config hash %q vs %q", base[AnnotationConfigHash], got[AnnotationConfigHash])
		})
	}
}

// TestReplicaChangeDoesNotRollThePods separates the two ways a StatefulSet
// converges: scaling rewrites a number, a template change restarts every pod.
func TestReplicaChangeDoesNotRollThePods(t *testing.T) {
	one := mustBuild(t, newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Replicas = 1 }))
	three := mustBuild(t, newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Replicas = 3 }))

	assert.Equal(t, one.Spec.Template.Annotations, three.Spec.Template.Annotations)
	assert.True(t, StatefulSetHasChanged(three, one), "the replica count still has to converge")
}

func TestStatefulSetHasChanged(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*appsv1.StatefulSet)
		want    bool
		because string
	}{
		{"identical", func(*appsv1.StatefulSet) {}, false, "a no-op update would restart nothing and cost a write"},
		{"replicas", func(s *appsv1.StatefulSet) { s.Spec.Replicas = ptr.To(int32(5)) }, true, ""},
		{"object label dropped", func(s *appsv1.StatefulSet) { delete(s.Labels, common.LabelVersion) }, true, ""},
		{"pod template label dropped", func(s *appsv1.StatefulSet) {
			delete(s.Spec.Template.Labels, common.LabelInstance)
		}, true, ""},
		{"pod spec hash", func(s *appsv1.StatefulSet) {
			s.Spec.Template.Annotations[AnnotationPodSpecHash] = "deadbeef"
		}, true, ""},
		{"config hash", func(s *appsv1.StatefulSet) {
			s.Spec.Template.Annotations[AnnotationConfigHash] = "deadbeef"
		}, true, ""},
		{"a foreign label added by somebody else", func(s *appsv1.StatefulSet) {
			s.Labels["example.com/owner"] = "platform"
		}, false, "reverting other people's labels is not this operator's job"},
		{"an annotation kubectl rollout restart added", func(s *appsv1.StatefulSet) {
			s.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = "2026-01-01T00:00:00Z"
		}, false, "only the two operator annotations decide whether the template drifted"},
		{"a defaulted pod field the operator never writes", func(s *appsv1.StatefulSet) {
			s.Spec.Template.Spec.DNSPolicy = corev1.DNSClusterFirst
			s.Spec.Template.Spec.SchedulerName = "default-scheduler"
			s.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyAlways
		}, false, "the API server defaults these, and reacting to them is a permanent update loop"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMosquitto()
			desired := mustBuild(t, m)
			current := mustBuild(t, m)
			tt.mutate(current)

			assert.Equal(t, tt.want, StatefulSetHasChanged(desired, current), tt.because)
		})
	}
}

func TestStatefulSetHasChanged_NilReplicasIsNotDrift(t *testing.T) {
	m := newMosquitto()
	desired := mustBuild(t, m)
	current := mustBuild(t, m)
	current.Spec.Replicas = nil

	assert.False(t, StatefulSetHasChanged(desired, current),
		"a nil replica count carries no information to compare against")
}
