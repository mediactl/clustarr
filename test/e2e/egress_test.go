//go:build e2e

/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// e2eNetworkPolicy is config/e2e/network-policy.yaml's NetworkPolicy.
const e2eNetworkPolicy = "e2e-no-internet"

// egressInternet are Internet addresses no pod in the e2e namespace may
// connect to. IP literals, so the check does not hang on DNS: a name would
// resolve (CoreDNS is outside the policy) and then be refused the same way.
var egressInternet = []string{"1.1.1.1:443", "8.8.8.8:443"}

// TestTheClusterReachesNoInternet proves config/e2e's NetworkPolicy is
// enforced, not merely installed: a pod in the e2e namespace connects to a
// stub (so a probe that can reach nothing never passes) and to no Internet
// address. kind's default CNI, kindnet, enforces NetworkPolicy through its
// embedded kube-network-policies controller (the policy's own header
// records the ruling); a CNI that ignored it would fail here, by name.
//
// It cannot tell an enforced policy from a host with no Internet at all;
// on such a host it passes for the weaker reason, which is the same verdict.
func TestTheClusterReachesNoInternet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var np networkingv1.NetworkPolicy
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: e2eNetworkPolicy}, &np),
		"config/e2e's NetworkPolicy %s is not installed", e2eNetworkPolicy)
	require.Contains(t, np.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: uniqueName("e2e-egress-probe"), Namespace: Namespace,
			Labels: map[string]string{PartOfLabel: PartOfValue, "app.kubernetes.io/component": "e2e-egress-probe"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   new(true),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:            "egress-probe",
				Image:           fixturesImage(ctx, t),
				ImagePullPolicy: corev1.PullIfNotPresent,
				Args: []string{
					"egress-probe",
					"--reach=torznab-stub." + Namespace + ".svc:80",
					"--refuse=" + strings.Join(egressInternet, ","),
					"--timeout=5s",
				},
				TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: new(false),
					ReadOnlyRootFilesystem:   new(true),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, pod))
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), pod) })

	var live corev1.Pod
	waitFor(t, ctx, 3*time.Minute, "egress probe pod "+pod.Name+" finished", func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return live.Status.Phase == corev1.PodSucceeded || live.Status.Phase == corev1.PodFailed, nil
	}, func() string { return fmt.Sprintf("pod %s phase=%s", pod.Name, live.Status.Phase) })
	require.Equalf(t, corev1.PodSucceeded, live.Status.Phase,
		"a pod in %s reached the Internet, or could not reach a stub:\n%s", Namespace, terminationMessage(&live))
}

// fixturesImage is the e2e fixture image the cluster runs, read from a stub
// Deployment: hack/e2e.sh may load it under another tag than config/e2e's.
func fixturesImage(ctx context.Context, t *testing.T) string {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "torznab-stub"}, &d))
	require.NotEmpty(t, d.Spec.Template.Spec.Containers)
	return d.Spec.Template.Spec.Containers[0].Image
}

// terminationMessage is a finished pod's containers' termination messages.
func terminationMessage(p *corev1.Pod) string {
	var out []string
	for _, cs := range p.Status.ContainerStatuses {
		if term := cs.State.Terminated; term != nil {
			out = append(out, fmt.Sprintf("%s exit %d: %s", cs.Name, term.ExitCode, term.Message))
		}
	}
	return strings.Join(out, "\n")
}
