package providerbroker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ImportOptions is `agent-project-controller provider import`: copy the
// operator's existing provider login from their own machine to the broker.
// It runs once, with the operator's own kubeconfig, and leaves nothing behind
// on the machine or in the cluster except the installed login.
type ImportOptions struct {
	Provider       string
	File           string // default: the provider CLI's own login file
	Namespace      string // the providers' namespace
	ReplaceAccount bool
	Wait           time.Duration
	Poll           time.Duration
}

// DefaultLoginFile is where each provider CLI keeps its login on Linux and macOS.
func DefaultLoginFile(provider string) string {
	home, _ := os.UserHomeDir()
	switch provider {
	case "grok":
		return filepath.Join(home, ".grok", "auth.json")
	case "cursor":
		return filepath.Join(home, ".config", "cursor", "auth.json")
	}
	return ""
}

// Import validates the local login, hands it to the broker as a one-shot
// Secret and waits for the broker to install it (or say why not).
func Import(ctx context.Context, c client.Client, o ImportOptions, out io.Writer) error {
	if o.Provider != "grok" && o.Provider != "cursor" {
		return errors.New("provider must be grok or cursor")
	}
	if o.File == "" {
		o.File = DefaultLoginFile(o.Provider)
	}
	if o.Wait == 0 {
		o.Wait = 90 * time.Second
	}
	if o.Poll == 0 {
		o.Poll = 2 * time.Second
	}
	raw, err := os.ReadFile(o.File)
	if err != nil {
		return fmt.Errorf("no %s login at %s; sign in with the %s CLI first, or pass --file", o.Provider, o.File, o.Provider)
	}
	if _, err := ValidateLogin(o.Provider, raw); err != nil {
		return err
	}
	fmt.Fprintf(out, "Copying your %s login from %s to Glovecell (namespace %s).\n", o.Provider, o.File, o.Namespace)
	fmt.Fprintln(out, "Note: this machine and Glovecell now share one sign-in session. If the provider rotates it, one of the two may be signed out; signing in from the console (Connect) gives Glovecell its own session instead.")

	key := types.NamespacedName{Namespace: o.Namespace, Name: ImportSecretName(o.Provider)}
	var existing corev1.Secret
	if err := c.Get(ctx, key, &existing); err == nil {
		if existing.Annotations[ImportErrorAnnotation] == "" {
			return errors.New("another import for this provider is still pending; wait for it or delete Secret " + key.Name)
		}
		_ = c.Delete(ctx, &existing)
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("cannot reach namespace %s: %v", o.Namespace, err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "glovecell-provider-import"}},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{ImportKey: raw},
	}
	if o.ReplaceAccount {
		secret.Annotations = map[string]string{ImportReplaceAnnotation: "true"}
	}
	if err := c.Create(ctx, secret); err != nil {
		return fmt.Errorf("could not hand the login to the broker: %v", err)
	}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}})
	}
	deadline := time.Now().Add(o.Wait)
	for {
		var current corev1.Secret
		err := c.Get(ctx, key, &current)
		if apierrors.IsNotFound(err) {
			fmt.Fprintf(out, "Done: the broker installed your %s login. Sandboxes that use %s get access within a minute.\n", o.Provider, o.Provider)
			return nil
		}
		if err == nil && current.Annotations[ImportErrorAnnotation] != "" {
			cleanup()
			return errors.New("the broker refused the login: " + current.Annotations[ImportErrorAnnotation])
		}
		if time.Now().After(deadline) {
			cleanup()
			return fmt.Errorf("the provider broker did not pick up the login within %s; check that providerBroker is enabled with %s and look at `kubectl -n %s logs deploy/provider-broker`", o.Wait, o.Provider, o.Namespace)
		}
		select {
		case <-ctx.Done():
			cleanup()
			return ctx.Err()
		case <-time.After(o.Poll):
		}
	}
}
