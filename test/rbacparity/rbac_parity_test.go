//go:build rbacparity

// Package rbacparity verifies that the two install paths grant the same authority.
//
// The operator ships two ways: `helm install` from deploy/helm/mosquitto-operator,
// and `kustomize build config/default | kubectl apply -f -`. Only the kustomize one
// is generated — `make generate-all` runs controller-gen over the kubebuilder
// markers and writes config/rbac/role.yaml. The chart's ClusterRole is written by
// hand, and nothing regenerates it.
//
// A new marker therefore updates one path and silently leaves the other behind,
// and the failure is nasty in both directions: too few verbs and the operator 403s
// on every pass for chart users only; too many and the chart hands out cluster-wide
// authority the code never asked for. Neither shows up anywhere else, because both
// are manifests — there is no compilation step to break.
//
// The comparison is over the RENDERED output of both paths, decoded into the real
// rbacv1 types rather than matched as text. Rule order, apiGroups grouped or split,
// verb order and the resource-name prefixes each path generates all wash out; what
// is compared is the set of (kind, apiGroup, resource) -> verbs.
//
// Behind the `rbacparity` build tag because it shells out to helm and kustomize,
// which the plain unit tier must not require. Run with `make verify-rbac-parity`.
package rbacparity

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

const (
	chartDir     = "deploy/helm/mosquitto-operator"
	kustomizeDir = "config/default"
	namespace    = "mosquitto-operator-system"
)

// grant is one (kind, apiGroup, resource) pair. The kind is part of the key on
// purpose: the same verbs on a namespaced Role and on a ClusterRole are not the
// same authority, and that difference is exactly what the leader-election rules
// got wrong once — the chart granted leases cluster-wide while config/rbac used a
// namespaced Role.
type grant struct {
	kind     string
	apiGroup string
	resource string
}

func (g grant) String() string {
	group := g.apiGroup
	if group == "" {
		group = "core"
	}
	return fmt.Sprintf("%s %s/%s", g.kind, group, g.resource)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err, "this test must run inside the repository")
	return strings.TrimSpace(string(out))
}

func render(t *testing.T, root string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "%s %s failed: %s", name, strings.Join(args, " "), stderr.String())
	return stdout.String()
}

// authority decodes every ClusterRole and Role in a multi-document manifest and
// collapses it to grant -> sorted verb set.
func authority(t *testing.T, manifest string) map[grant][]string {
	t.Helper()
	grants := map[grant]map[string]struct{}{}

	for _, doc := range strings.Split(manifest, "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		// Decode only far enough to learn the kind; anything else in the stream
		// (Deployment, Service, CRD) is skipped without being parsed as RBAC.
		var probe struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal([]byte(doc), &probe); err != nil {
			continue
		}
		if probe.Kind != "ClusterRole" && probe.Kind != "Role" {
			continue
		}

		// ClusterRole and Role carry an identical Rules field, so one type decodes
		// both — the kind is kept separately in the grant key.
		var role rbacv1.ClusterRole
		require.NoError(t, yaml.Unmarshal([]byte(doc), &role), "decoding a %s", probe.Kind)

		for _, rule := range role.Rules {
			groups := rule.APIGroups
			if len(groups) == 0 {
				groups = []string{""}
			}
			for _, group := range groups {
				for _, resource := range rule.Resources {
					key := grant{kind: probe.Kind, apiGroup: group, resource: resource}
					if grants[key] == nil {
						grants[key] = map[string]struct{}{}
					}
					for _, verb := range rule.Verbs {
						grants[key][verb] = struct{}{}
					}
				}
			}
		}
	}

	out := make(map[grant][]string, len(grants))
	for key, verbs := range grants {
		list := make([]string, 0, len(verbs))
		for verb := range verbs {
			list = append(list, verb)
		}
		sort.Strings(list)
		out[key] = list
	}
	return out
}

// installSetting is one install-time setting both paths must render alike: the
// Helm values that select it and the kustomize components that do.
type installSetting struct {
	name           string
	helmSet        []string
	components     []string
	secretSecurity string
}

var installSettings = []installSetting{
	{name: "defaults", secretSecurity: "false"},
	{
		// docs/adr/0014 D10: the switch adds get on secrets, on both paths.
		name:           "secretSecurity",
		helmSet:        []string{"secretSecurity=true"},
		components:     []string{"config/components/secret-security"},
		secretSecurity: "true",
	},
}

// kustomizeTarget returns the directory to build for a setting: config/default
// itself, or an overlay of it with the setting's components, written under the
// repository's tmp/ so its relative paths stay inside the repository.
func kustomizeTarget(t *testing.T, root string, setting installSetting) string {
	t.Helper()
	if len(setting.components) == 0 {
		return kustomizeDir
	}
	dir := filepath.Join("tmp", "rbacparity-"+setting.name)
	require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o750))
	var b strings.Builder
	b.WriteString("resources:\n  - ../../" + kustomizeDir + "\ncomponents:\n")
	for _, component := range setting.components {
		b.WriteString("  - ../../" + component + "\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, dir, "kustomization.yaml"), []byte(b.String()), 0o600))
	return dir
}

// effectiveFlag returns the value the manager container of a rendered manifest
// passes for --<name>=, taking the last occurrence as Go's flag package does,
// or "" when the flag is absent.
func effectiveFlag(t *testing.T, manifest, name string) string {
	t.Helper()
	value := ""
	for _, doc := range strings.Split(manifest, "\n---") {
		var deployment struct {
			Kind string `json:"kind"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Name string   `json:"name"`
							Args []string `json:"args"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		if yaml.Unmarshal([]byte(doc), &deployment) != nil || deployment.Kind != "Deployment" {
			continue
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			for _, arg := range container.Args {
				if v, ok := strings.CutPrefix(arg, "--"+name+"="); ok && container.Name == "manager" {
					value = v
				}
			}
		}
	}
	return value
}

func TestRBACParity_BothInstallPathsGrantTheSameAuthority(t *testing.T) {
	root := repoRoot(t)

	kustomize := filepath.Join(root, "bin", "kustomize")
	if _, err := os.Stat(kustomize); err != nil {
		resolved, lookErr := exec.LookPath("kustomize")
		require.NoError(t, lookErr, "kustomize not found in bin/ or on PATH - run `make kustomize`")
		kustomize = resolved
	}

	for _, setting := range installSettings {
		t.Run(setting.name, func(t *testing.T) {
			helmArgs := []string{"template", "parity", chartDir, "--namespace", namespace}
			for _, set := range setting.helmSet {
				helmArgs = append(helmArgs, "--set", set)
			}
			helmOut := render(t, root, "helm", helmArgs...)
			kustomizeOut := render(t, root, kustomize, "build", kustomizeTarget(t, root, setting))

			compareAuthority(t, authority(t, helmOut), authority(t, kustomizeOut))

			// The rule is only half of a setting: the flag that makes the operator
			// use it has to reach the manager on both paths as well.
			require.Equal(t, setting.secretSecurity, effectiveFlag(t, helmOut, "secret-security"),
				"the chart passes the wrong --secret-security")
			require.Equal(t, setting.secretSecurity, effectiveFlag(t, kustomizeOut, "secret-security"),
				"kustomize passes the wrong --secret-security")
		})
	}
}

// compareAuthority fails on every grant the two install paths render differently.
func compareAuthority(t *testing.T, helm, kust map[grant][]string) {
	t.Helper()

	// A decoder that silently reads nothing would make this test pass forever.
	// Both paths are known to define RBAC, so an empty side is a broken test, not
	// a passing one.
	require.NotEmpty(t, helm, "parsed no RBAC rules from the Helm output")
	require.NotEmpty(t, kust, "parsed no RBAC rules from the kustomize output")

	keys := map[grant]struct{}{}
	for k := range helm {
		keys[k] = struct{}{}
	}
	for k := range kust {
		keys[k] = struct{}{}
	}
	ordered := make([]grant, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].String() < ordered[j].String() })

	const hint = "config/rbac/role.yaml is generated from the kubebuilder markers by " +
		"`make generate-all`; deploy/helm/mosquitto-operator/templates/clusterrole.yaml is " +
		"written by hand, and a conditional rule lives in a chart value and in a component " +
		"under config/components/. After changing a marker or a setting, mirror it."

	for _, key := range ordered {
		h, inHelm := helm[key]
		k, inKust := kust[key]
		switch {
		case !inHelm:
			t.Errorf("%s: granted by kustomize (%s) but not by the chart.\n%s",
				key, strings.Join(k, ","), hint)
		case !inKust:
			t.Errorf("%s: granted by the chart (%s) but not by kustomize.\n%s",
				key, strings.Join(h, ","), hint)
		default:
			require.Equal(t, k, h, "%s: the two install paths grant different verbs.\n%s", key, hint)
		}
	}

	t.Logf("compared %d grants across both install paths", len(ordered))
}
