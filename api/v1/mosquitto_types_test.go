package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

func TestAntiAffinityMode(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want string
	}{
		{"empty falls back to off", "", AntiAffinityModeOff},
		{"off stays off", AntiAffinityModeOff, AntiAffinityModeOff},
		{"soft", AntiAffinityModeSoft, AntiAffinityModeSoft},
		{"hard", AntiAffinityModeHard, AntiAffinityModeHard},
		{"a value the enum would have rejected degrades to off", "strict", AntiAffinityModeOff},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Mosquitto{Spec: MosquittoSpec{AntiAffinity: tt.spec}}
			assert.Equal(t, tt.want, m.AntiAffinityMode())
		})
	}
}

func TestIsTLSEnabled(t *testing.T) {
	tests := []struct {
		name string
		tls  *MosquittoTLS
		want bool
	}{
		{"no tls block", nil, false},
		{"empty secret name is not a usable listener", &MosquittoTLS{SecretName: ""}, false},
		{"secret name set", &MosquittoTLS{SecretName: "broker-tls"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Mosquitto{Spec: MosquittoSpec{TLS: tt.tls}}
			assert.Equal(t, tt.want, m.IsTLSEnabled())
		})
	}
}

func TestIsStorageEnabled(t *testing.T) {
	tests := []struct {
		name    string
		storage *MosquittoStorage
		want    bool
	}{
		{"no storage block means emptyDir", nil, false},
		{"empty size is not a claim", &MosquittoStorage{Size: ""}, false},
		{"size set", &MosquittoStorage{Size: "1Gi"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Mosquitto{Spec: MosquittoSpec{Storage: tt.storage}}
			assert.Equal(t, tt.want, m.IsStorageEnabled())
		})
	}
}

// TestDeepCopyIsIndependent guards the generated deepcopy: the manager cache
// hands out shared pointers, so a reconciler that mutates a copy must not reach
// the cached object through a shared pointer field.
func TestDeepCopyIsIndependent(t *testing.T) {
	original := &Mosquitto{
		ObjectMeta: metav1.ObjectMeta{Name: "broker", Namespace: "ns"},
		Spec: MosquittoSpec{
			Replicas: 3,
			TLS:      &MosquittoTLS{SecretName: "broker-tls"},
			Storage:  &MosquittoStorage{Size: "1Gi", StorageClassName: ptr.To("fast")},
		},
		Status: MosquittoStatus{
			Phase:      PhaseReady,
			Conditions: []metav1.Condition{{Type: ConditionTypeReady, Status: metav1.ConditionTrue}},
		},
	}

	clone := original.DeepCopy()
	clone.Spec.TLS.SecretName = "other-tls"
	clone.Spec.Storage.Size = "5Gi"
	*clone.Spec.Storage.StorageClassName = "slow"
	clone.Status.Conditions[0].Status = metav1.ConditionFalse

	assert.Equal(t, "broker-tls", original.Spec.TLS.SecretName)
	assert.Equal(t, "1Gi", original.Spec.Storage.Size)
	assert.Equal(t, "fast", *original.Spec.Storage.StorageClassName)
	assert.Equal(t, metav1.ConditionTrue, original.Status.Conditions[0].Status)
}

func TestDeepCopyObjectReturnsSameType(t *testing.T) {
	obj := (&Mosquitto{ObjectMeta: metav1.ObjectMeta{Name: "broker"}}).DeepCopyObject()
	m, ok := obj.(*Mosquitto)
	require.True(t, ok, "DeepCopyObject must return a *Mosquitto")
	assert.Equal(t, "broker", m.Name)

	listObj := (&MosquittoList{Items: []Mosquitto{{}}}).DeepCopyObject()
	list, ok := listObj.(*MosquittoList)
	require.True(t, ok, "DeepCopyObject must return a *MosquittoList")
	assert.Len(t, list.Items, 1)
}

// TestAddToSchemeRegistersBothKinds guards the group-version wiring: without it
// the manager cannot build a cache for the type and fails with "no kind is
// registered".
func TestAddToSchemeRegistersBothKinds(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))

	assert.True(t, scheme.Recognizes(GroupVersion.WithKind("Mosquitto")))
	assert.True(t, scheme.Recognizes(GroupVersion.WithKind("MosquittoList")))
	assert.Equal(t, "mko.gtrfc.com", GroupVersion.Group)
	assert.Equal(t, "v1", GroupVersion.Version)
}

// TestSubTypeDeepCopy exercises the generated copiers of the nested types
// directly. They are what a caller reaches for when it copies a spec fragment
// rather than the whole resource, and a shallow copy there aliases the cached
// object just as badly.
func TestSubTypeDeepCopy(t *testing.T) {
	t.Run("spec", func(t *testing.T) {
		spec := &MosquittoSpec{
			Replicas: 2,
			TLS:      &MosquittoTLS{SecretName: "broker-tls"},
			Storage:  &MosquittoStorage{Size: "1Gi", StorageClassName: ptr.To("fast")},
		}
		clone := spec.DeepCopy()
		clone.TLS.SecretName = "other"
		clone.Storage.Size = "2Gi"

		assert.Equal(t, "broker-tls", spec.TLS.SecretName)
		assert.Equal(t, "1Gi", spec.Storage.Size)
	})

	t.Run("spec without optional blocks", func(t *testing.T) {
		clone := (&MosquittoSpec{Replicas: 1}).DeepCopy()
		assert.Nil(t, clone.TLS)
		assert.Nil(t, clone.Storage)
	})

	t.Run("status", func(t *testing.T) {
		status := &MosquittoStatus{
			Phase:      PhaseProgressing,
			Conditions: []metav1.Condition{{Type: ConditionTypeReady, Reason: "ReplicasNotReady"}},
		}
		clone := status.DeepCopy()
		clone.Conditions[0].Reason = "Other"

		assert.Equal(t, "ReplicasNotReady", status.Conditions[0].Reason)
		assert.Nil(t, (&MosquittoStatus{}).DeepCopy().Conditions)
	})

	t.Run("tls", func(t *testing.T) {
		tls := &MosquittoTLS{SecretName: "broker-tls"}
		assert.Equal(t, tls, tls.DeepCopy())
	})

	t.Run("storage", func(t *testing.T) {
		storage := &MosquittoStorage{Size: "1Gi", StorageClassName: ptr.To("fast")}
		clone := storage.DeepCopy()
		*clone.StorageClassName = "slow"

		assert.Equal(t, "fast", *storage.StorageClassName)
		assert.Nil(t, (&MosquittoStorage{Size: "1Gi"}).DeepCopy().StorageClassName)
	})

	t.Run("list", func(t *testing.T) {
		list := &MosquittoList{Items: []Mosquitto{{ObjectMeta: metav1.ObjectMeta{Name: "broker"}}}}
		clone := list.DeepCopy()
		clone.Items[0].Name = "other"

		assert.Equal(t, "broker", list.Items[0].Name)
		assert.Nil(t, (&MosquittoList{}).DeepCopy().Items)
	})
}
