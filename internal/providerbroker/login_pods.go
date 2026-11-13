package providerbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/connect"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// LoginLabel names the provider of a login pod; the chart's per-provider
	// network policy selects it.
	LoginLabel     = "projects.agentwork.dev/login"
	loginContainer = "login"
)

// LoginPodName is the fixed name of a provider's login pod.
func LoginPodName(provider string) string { return provider + "-login" }

// loginCommands run the stock provider CLIs directly (the workspace image's
// launchers refuse sign-in on purpose).
var loginCommands = map[string][]string{
	"grok":   {"/opt/grok/grok", "login", "--device-auth"},
	"cursor": {"/opt/cursor-agent/cursor-agent", "login"},
}

const loginRead = `import pathlib,json,sys
provider=json.load(sys.stdin)['provider'];assert provider in ('grok','cursor')
p=pathlib.Path.home()/('.grok/auth.json' if provider=='grok' else '.config/cursor/auth.json')
assert not p.is_symlink();raw=p.read_bytes();assert 0<len(raw)<131072
sys.stdout.write(raw.decode())
`

// KubePods creates login pods from a fixed template (PROVIDER_LOGIN_POD,
// rendered by the chart). The broker sets only the name and labels; an
// admission policy refuses any other pod it tries to create.
type KubePods struct {
	Client    client.Client
	Config    *rest.Config
	Namespace string
	Template  corev1.PodSpec
}

func ParseLoginTemplate(raw string) (corev1.PodSpec, error) {
	var spec corev1.PodSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil || len(spec.Containers) != 1 || spec.Containers[0].Name != loginContainer || len(spec.Volumes) == 0 {
		return corev1.PodSpec{}, errors.New("PROVIDER_LOGIN_POD must be a pod spec with one container named login")
	}
	for _, v := range spec.Volumes {
		if v.EmptyDir == nil {
			return corev1.PodSpec{}, errors.New("login pods may only use emptyDir volumes")
		}
	}
	return spec, nil
}

func (k KubePods) pod(provider string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: LoginPodName(provider), Namespace: k.Namespace,
		Labels: map[string]string{"app.kubernetes.io/name": "credential-login", LoginLabel: provider}}, Spec: *k.Template.DeepCopy()}
}

func (k KubePods) Start(ctx context.Context, provider string) error {
	if err := k.Delete(ctx, provider); err != nil {
		return err
	}
	if err := k.Client.Create(ctx, k.pod(provider)); err != nil {
		return err
	}
	for {
		var pod corev1.Pod
		if err := k.Client.Get(ctx, types.NamespacedName{Namespace: k.Namespace, Name: LoginPodName(provider)}, &pod); err == nil {
			switch pod.Status.Phase {
			case corev1.PodRunning:
				return nil
			case corev1.PodFailed, corev1.PodSucceeded:
				return errors.New("login pod stopped")
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (k KubePods) Run(ctx context.Context, provider string, output io.Writer) error {
	return connect.ExecInContainer(ctx, k.Config, k.Namespace, LoginPodName(provider), loginContainer, loginCommands[provider], nil, output, output, false)
}

func (k KubePods) Read(ctx context.Context, provider string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	input, _ := json.Marshal(map[string]string{"provider": provider})
	var out bytes.Buffer
	if err := connect.ExecInContainer(ctx, k.Config, k.Namespace, LoginPodName(provider), loginContainer, []string{"python3", "-c", loginRead}, bytes.NewReader(input), &out, io.Discard, false); err != nil || out.Len() > maxLogin {
		return nil, errors.New("login file unavailable")
	}
	return out.Bytes(), nil
}

// Delete removes the login pod and waits until it is gone.
func (k KubePods) Delete(ctx context.Context, provider string) error {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: LoginPodName(provider), Namespace: k.Namespace}}
	zero := int64(0)
	if err := k.Client.Delete(ctx, pod, &client.DeleteOptions{GracePeriodSeconds: &zero}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	for {
		if err := k.Client.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}); apierrors.IsNotFound(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
