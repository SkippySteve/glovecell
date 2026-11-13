package providerbroker

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Enrollment puts a new provider login into the offline owner. There are two
// sources, and both end in install:
//
//   - a sign-in the operator approves in the console (login.go), run in a
//     short-lived pod that holds no existing login;
//   - a login file the operator copies from their own machine with
//     `agent-project-controller provider import`, delivered as a one-shot
//     Secret (ImportSecretName) that the broker consumes on its next tick.
//
// install validates the document, refuses a different Cursor account unless
// the operator asked to replace it, clears any interrupted renewal for that
// provider first (so a saved older response can never overwrite the new
// login), writes the file atomically in the owner, and reads it back through
// the normal renewal path.

const (
	// ImportKey is the one data key of an import Secret.
	ImportKey = "auth.json"
	// ImportReplaceAnnotation ("true") allows a different Cursor account.
	ImportReplaceAnnotation = "providers.agentwork.dev/replace-account"
	// ImportErrorAnnotation records why the broker refused an import. The
	// importing operator deletes the Secret after reading it.
	ImportErrorAnnotation = "providers.agentwork.dev/import-error"
	maxLogin              = 128 << 10
)

// ErrAccountChanged refuses a Cursor login for a different account than the
// pinned one, unless the operator asked to replace the account.
var ErrAccountChanged = errors.New("this is a different Cursor account than the one already signed in; choose Replace account to switch")

// ImportSecretName is the one-shot Secret an operator creates to import a login.
func ImportSecretName(provider string) string { return "provider-import-" + provider }

// ValidateLogin checks a provider login file as the provider's CLI writes it
// and returns the account subject (Cursor only). Messages never contain
// credential material.
func ValidateLogin(provider string, raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > maxLogin {
		return "", errors.New("login file is empty or too large")
	}
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil || document == nil {
		return "", errors.New("login file is not a JSON object")
	}
	switch provider {
	case "grok":
		if len(document) != 1 {
			return "", errors.New("Grok login must hold exactly one account")
		}
		for _, v := range document {
			entry, _ := v.(map[string]any)
			issuer, _ := entry["oidc_issuer"].(string)
			key, _ := entry["key"].(string)
			refresh, _ := entry["refresh_token"].(string)
			id, _ := entry["oidc_client_id"].(string)
			expires, _ := entry["expires_at"].(string)
			if issuer != "https://auth.x.ai" {
				return "", errors.New("Grok login is not from auth.x.ai")
			}
			if key == "" || refresh == "" || id == "" {
				return "", errors.New("Grok login lacks its access or refresh token; sign in to Grok again")
			}
			if _, err := time.Parse(time.RFC3339Nano, expires); err != nil {
				return "", errors.New("Grok login has no valid expiry")
			}
		}
		return "", nil
	case "cursor":
		token, _ := document["accessToken"].(string)
		refresh, _ := document["refreshToken"].(string)
		c, err := claims(token)
		if err != nil || c.Sub == "" {
			return "", errors.New("Cursor login has no valid access token")
		}
		if refresh == "" {
			return "", errors.New("Cursor login lacks its refresh token; sign in to Cursor again")
		}
		return c.Sub, nil
	}
	return "", errors.New("unsupported provider")
}

// ownerInstall writes a validated login into the owner. It refuses while a
// provider CLI runs there and replaces the file atomically with mode 0600.
const ownerInstall = `import pathlib,json,hashlib,os,sys
x=json.load(sys.stdin);provider=x['provider'];assert provider in ('grok','cursor')
p=pathlib.Path.home()/('.grok/auth.json' if provider=='grok' else '.config/cursor/auth.json')
for proc in pathlib.Path('/proc').iterdir():
 if proc.name.isdigit():
  try: assert (proc/'comm').read_text().strip() not in ('cursor-agent','grok'),'owner busy'
  except (OSError,NotADirectoryError):pass
p.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
assert not p.is_symlink() and not p.parent.is_symlink()
raw=json.dumps(x['document']).encode()
t=p.with_name('.provider-broker-install.tmp')
fd=os.open(t,os.O_WRONLY|os.O_CREAT|os.O_TRUNC|os.O_NOFOLLOW,0o600)
with os.fdopen(fd,'wb') as f:f.write(raw);f.flush();os.fsync(f.fileno())
os.replace(t,p)
d=os.open(p.parent,os.O_RDONLY);os.fsync(d);os.close(d)
p.with_name('.apc-oauth-commit.json').unlink(missing_ok=True)
print(json.dumps({'sha256':hashlib.sha256(p.read_bytes()).hexdigest()}))
`

// Install enrolls a login and publishes fresh leases right away.
func (b *Broker) Install(ctx context.Context, provider string, raw []byte, replace bool) error {
	ownerMu.Lock()
	defer ownerMu.Unlock()
	if err := b.install(ctx, provider, raw, replace); err != nil {
		return err
	}
	_ = b.TickLeases(ctx) // the other provider's state is reported by the next tick
	return nil
}

func (b *Broker) install(ctx context.Context, provider string, raw []byte, replace bool) error {
	if !slices.Contains(b.Config.Providers, provider) {
		return errors.New("provider is not enabled on this broker")
	}
	owner, ok := b.Config.Owners[provider]
	if !ok {
		return errors.New("credential owner not configured")
	}
	subject, err := ValidateLogin(provider, raw)
	if err != nil {
		return err
	}
	journal, err := b.boundSecret(ctx, b.Config.Journal)
	if err != nil {
		return err
	}
	if provider == "cursor" {
		if pinned := string(journal.Data[cursorSubjectKey]); pinned != "" && pinned != subject && !replace {
			return ErrAccountChanged
		}
	}
	// Clear the interrupted renewal (and, when replacing, the account pin)
	// before writing, so recovery can never write an older login back.
	before := journal.DeepCopy()
	delete(journal.Data, provider+"-renewal")
	if provider == "cursor" && replace {
		delete(journal.Data, cursorSubjectKey)
	}
	if len(journal.Data) != len(before.Data) {
		if b.Client.Patch(ctx, journal, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})) != nil {
			return errors.New("renewal journal busy; try again")
		}
	}
	var document map[string]any
	_ = json.Unmarshal(raw, &document)
	input, _ := json.Marshal(map[string]any{"provider": provider, "document": document})
	if _, err := b.Execute(ctx, owner, ownerInstall, input); err != nil {
		return errors.New("credential owner unavailable or busy; try again")
	}
	access, err := b.owner(ctx, provider)
	if err != nil || !validAccess(access) {
		return errors.New("login saved but could not be renewed; sign in again")
	}
	if provider == "cursor" {
		return b.pinCursor(ctx, access.Subject)
	}
	return nil
}

// importPending consumes operator import Secrets. A refused import keeps its
// Secret with the reason so the importing command can report it; the broker
// skips it until the operator deletes it.
func (b *Broker) importPending(ctx context.Context) {
	for _, provider := range b.Config.Providers {
		var secret corev1.Secret
		key := types.NamespacedName{Namespace: b.Config.Control, Name: ImportSecretName(provider)}
		if err := b.Client.Get(ctx, key, &secret); err != nil {
			continue
		}
		if secret.Annotations[ImportErrorAnnotation] != "" || !secret.DeletionTimestamp.IsZero() {
			continue
		}
		var err error
		if raw, ok := secret.Data[ImportKey]; !ok || len(secret.Data) != 1 {
			err = errors.New("import Secret must hold exactly one key, " + ImportKey)
		} else {
			err = b.install(ctx, provider, raw, secret.Annotations[ImportReplaceAnnotation] == "true")
		}
		if err == nil {
			uid := secret.UID
			if e := b.Client.Delete(ctx, &secret, client.Preconditions{UID: &uid}); e != nil && !apierrors.IsNotFound(e) {
				b.log(ctx, "provider login imported; its import Secret could not be deleted", provider)
			} else {
				b.log(ctx, "provider login imported", provider)
			}
			continue
		}
		before := secret.DeepCopy()
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[ImportErrorAnnotation] = err.Error()
		_ = b.Client.Patch(ctx, &secret, client.MergeFrom(before))
		b.log(ctx, "provider login import refused", provider)
	}
}
