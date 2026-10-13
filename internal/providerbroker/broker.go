// Package providerbroker renews access grants on the trusted control side.
// It never runs in a coding worker or exposes refresh material to one.
package providerbroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/connect"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Owner struct{ Namespace, Pod, Container string }
type Config struct {
	Control, Workspace, UID, Journal string
	Owners                           map[string]Owner
}
type Access struct {
	Token   string  `json:"accessToken"`
	Expires float64 `json:"expiresAt"`
	Issuer  string  `json:"issuer,omitempty"`
}
type Grant struct {
	Enabled bool    `json:"enabled"`
	Access  *Access `json:"access,omitempty"`
}
type Broker struct {
	Client  client.Client
	Config  Config
	Execute func(context.Context, Owner, string, []byte) ([]byte, error)
	HTTP    *http.Client
	Now     func() time.Time
}

func validAccess(value Access) bool {
	if len(value.Token) == 0 || len(value.Token) > 32768 || math.IsNaN(value.Expires) || math.IsInf(value.Expires, 0) || value.Expires <= 0 {
		return false
	}
	for _, c := range value.Token {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func enrolledGrants(raw []byte) (map[string]Grant, error) {
	if len(raw) > 128<<10 {
		return nil, errors.New("provider enrollment projection too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var grants map[string]Grant
	if decoder.Decode(&grants) != nil || grants == nil {
		return nil, errors.New("invalid provider enrollment projection")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, errors.New("invalid provider enrollment projection")
	}
	for name, grant := range grants {
		if name != "grok" && name != "cursor" && name != "muse" {
			return nil, errors.New("unreviewed provider enrollment")
		}
		if !grant.Enabled {
			if grant.Access != nil {
				return nil, errors.New("revoked provider retained credentials")
			}
			continue
		}
		if grant.Access == nil || !validAccess(*grant.Access) || (name == "grok" && grant.Access.Issuer != "https://auth.x.ai") || (name != "grok" && grant.Access.Issuer != "") || (name == "muse" && grant.Access.Token != "apc-muse-subscription-gateway") {
			return nil, errors.New("invalid access-only provider enrollment")
		}
	}
	return grants, nil
}

const ownerRead = `import pathlib,json,hashlib,sys
p=json.load(sys.stdin)['provider'];f=pathlib.Path.home()/('.grok/auth.json' if p=='grok' else '.config/cursor/auth.json')
s=f.stat();assert s.st_uid==1000 and not s.st_mode&0o077
raw=f.read_bytes();assert len(raw)<131072
v=json.loads(raw)
if p=='cursor': v={'accessToken':v['accessToken']}
print(json.dumps({'sha256':hashlib.sha256(raw).hexdigest(),'document':v}))
`
const ownerWrite = `import pathlib,json,hashlib,os,sys
x=json.load(sys.stdin);p=pathlib.Path.home()/'.grok/auth.json';raw=p.read_bytes()
assert hashlib.sha256(raw).hexdigest()==x['beforeSHA256'] or json.loads(raw)==x['document'],'owner changed'
for proc in pathlib.Path('/proc').iterdir():
 if proc.name.isdigit():
  try: assert (proc/'comm').read_text().strip()!='grok','owner busy'
  except (OSError,NotADirectoryError):pass
temporary=p.with_name('.provider-broker-auth.tmp')
fd=os.open(temporary,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
with os.fdopen(fd,'w') as f: json.dump(x['document'],f);f.flush();os.fsync(f.fileno())
os.replace(temporary,p)
print('saved')
`

type snapshot struct {
	SHA      string                    `json:"sha256"`
	Document map[string]map[string]any `json:"document"`
}
type renewal struct {
	Stage    string                    `json:"stage"`
	Before   string                    `json:"beforeSHA256"`
	Document map[string]map[string]any `json:"document"`
}

func (b *Broker) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}
func (b *Broker) owner(ctx context.Context, provider string) (Access, error) {
	owner, ok := b.Config.Owners[provider]
	if !ok {
		return Access{}, errors.New("credential owner not configured")
	}
	input, _ := json.Marshal(map[string]string{"provider": provider})
	raw, err := b.Execute(ctx, owner, ownerRead, input)
	if err != nil || len(raw) > 128<<10 {
		return Access{}, errors.New("credential owner unavailable")
	}
	if provider == "cursor" {
		var current struct {
			Document struct {
				Token string `json:"accessToken"`
			} `json:"document"`
		}
		if json.Unmarshal(raw, &current) != nil {
			return Access{}, errors.New("invalid owner projection")
		}
		parts := strings.Split(current.Document.Token, "::")
		jwt := strings.Split(parts[len(parts)-1], ".")
		if len(jwt) != 3 {
			return Access{}, errors.New("invalid owner token")
		}
		payload, err := base64.RawURLEncoding.DecodeString(jwt[1])
		if err != nil {
			return Access{}, errors.New("invalid owner expiry")
		}
		var claims struct {
			Exp float64 `json:"exp"`
		}
		if json.Unmarshal(payload, &claims) != nil {
			return Access{}, errors.New("invalid owner expiry")
		}
		return Access{Token: current.Document.Token, Expires: claims.Exp}, nil
	}
	var current snapshot
	if json.Unmarshal(raw, &current) != nil || len(current.Document) != 1 {
		return Access{}, errors.New("invalid Grok owner snapshot")
	}
	var value map[string]any
	for _, v := range current.Document {
		value = v
	}
	if value["oidc_issuer"] != "https://auth.x.ai" {
		return Access{}, errors.New("unreviewed Grok issuer")
	}
	parse := func(v map[string]any) (Access, error) {
		expires, _ := v["expires_at"].(string)
		when, e := time.Parse(time.RFC3339Nano, expires)
		token, _ := v["key"].(string)
		if e != nil || token == "" {
			return Access{}, errors.New("invalid Grok owner access")
		}
		return Access{Token: token, Expires: float64(when.Unix()), Issuer: "https://auth.x.ai"}, nil
	}
	access, err := parse(value)
	if err != nil {
		return Access{}, err
	}
	var ledger corev1.Secret
	key := types.NamespacedName{Namespace: b.Config.Control, Name: b.Config.Journal}
	if err := b.Client.Get(ctx, key, &ledger); err != nil {
		return Access{}, errors.New("renewal journal unavailable")
	}
	if ledger.Labels["providers.agentwork.dev/workspace-uid"] != b.Config.UID {
		return Access{}, errors.New("renewal journal binding changed")
	}
	var pending renewal
	if saved := ledger.Data["grok-renewal"]; len(saved) > 0 {
		if json.Unmarshal(saved, &pending) != nil || pending.Stage != "response-saved" {
			return Access{}, errors.New("interrupted OAuth request requires owner recovery")
		}
	} else {
		if access.Expires > float64(b.now().Add(10*time.Minute).Unix()) {
			return access, nil
		}
		refresh, _ := value["refresh_token"].(string)
		id, _ := value["oidc_client_id"].(string)
		if refresh == "" || id == "" {
			return Access{}, errors.New("Grok owner sign-in required")
		}
		pending = renewal{Stage: "request-started", Before: current.SHA, Document: current.Document}
		if err := b.save(ctx, &ledger, pending); err != nil {
			return Access{}, err
		}
		response, err := b.refresh(ctx, refresh, id)
		if err != nil {
			return Access{}, err
		}
		value["key"] = response.Token
		if response.Refresh != "" {
			value["refresh_token"] = response.Refresh
		}
		value["expires_at"] = b.now().Add(time.Duration(response.Lifetime) * time.Second).UTC().Format(time.RFC3339Nano)
		pending.Stage = "response-saved"
		if err := b.save(ctx, &ledger, pending); err != nil {
			return Access{}, err
		}
	}
	input, _ = json.Marshal(pending)
	if _, err := b.Execute(ctx, owner, ownerWrite, input); err != nil {
		return Access{}, errors.New("owner renewal commit pending")
	}
	for _, v := range pending.Document {
		access, err = parse(v)
	}
	if err != nil {
		return Access{}, err
	}
	before := ledger.DeepCopy()
	delete(ledger.Data, "grok-renewal")
	if b.Client.Patch(ctx, &ledger, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})) != nil {
		return Access{}, errors.New("owner renewal receipt pending")
	}
	return access, nil
}

func (b *Broker) save(ctx context.Context, ledger *corev1.Secret, pending renewal) error {
	before := ledger.DeepCopy()
	raw, _ := json.Marshal(pending)
	if ledger.Data == nil {
		ledger.Data = map[string][]byte{}
	}
	ledger.Data["grok-renewal"] = raw
	if b.Client.Patch(ctx, ledger, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})) != nil {
		return errors.New("OAuth journal update failed")
	}
	return nil
}

type oauthResponse struct {
	Token    string  `json:"access_token"`
	Refresh  string  `json:"refresh_token"`
	Lifetime float64 `json:"expires_in"`
}

func (b *Broker) refresh(ctx context.Context, refresh, id string) (oauthResponse, error) {
	body := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {id}}
	request, _ := http.NewRequestWithContext(ctx, "POST", "https://auth.x.ai/oauth2/token", strings.NewReader(body.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpClient := &http.Client{Timeout: 25 * time.Second}
	if b.HTTP != nil {
		*httpClient = *b.HTTP
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("credential redirects denied") }
	response, err := httpClient.Do(request)
	if err != nil {
		return oauthResponse{}, errors.New("Grok OAuth renewal unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return oauthResponse{}, errors.New("Grok owner reauthorization required")
	}
	var value oauthResponse
	if json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&value) != nil || value.Token == "" || math.IsNaN(value.Lifetime) || math.IsInf(value.Lifetime, 0) || value.Lifetime < 120 || value.Lifetime > 86400*30 {
		return oauthResponse{}, errors.New("invalid OAuth response")
	}
	return value, nil
}

// Tick renews only grants that the operator has already enabled. Revocation is
// durable across publisher restarts. A failed owner cannot prevent another
// provider from renewing. Leases expire if this trusted publisher is unavailable.
func (b *Broker) Tick(ctx context.Context) error {
	var work api.ProjectWorkspace
	if b.Client.Get(ctx, types.NamespacedName{Namespace: b.Config.Control, Name: b.Config.Workspace}, &work) != nil || string(work.UID) != b.Config.UID || work.Spec.DesiredState == "Removed" || !work.DeletionTimestamp.IsZero() {
		return errors.New("workbench unavailable or removed")
	}
	namespace := "apc-" + strings.ReplaceAll(b.Config.UID, "-", "")
	var secret corev1.Secret
	if b.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "model-access"}, &secret) != nil || secret.Labels[api.OwnerLabel] != b.Config.UID {
		return errors.New("workbench access projection unavailable")
	}
	grants, err := enrolledGrants(secret.Data["provider-access"])
	if err != nil {
		return errors.New("provider enrollment projection unavailable")
	}
	var failures []error
	for _, name := range []string{"grok", "cursor"} {
		grant := grants[name]
		if !grant.Enabled {
			continue
		}
		access, err := b.owner(ctx, name)
		if err != nil || !validAccess(access) || access.Expires <= float64(b.now().Add(time.Minute).Unix()) {
			failures = append(failures, errors.New(name+" credential owner renewal unavailable"))
			continue
		}
		access.Expires = math.Min(access.Expires, float64(b.now().Add(5*time.Minute).Unix()))
		grants[name] = Grant{Enabled: true, Access: &access}
	}
	// Re-read lifecycle and projection before publication. Never undo a revocation
	// made while a refresh request was in flight or replace another provider's data.
	var current api.ProjectWorkspace
	if b.Client.Get(ctx, client.ObjectKeyFromObject(&work), &current) != nil || current.UID != work.UID || current.Spec.DesiredState == "Removed" || !current.DeletionTimestamp.IsZero() {
		return errors.New("workbench changed during renewal")
	}
	var latest corev1.Secret
	if b.Client.Get(ctx, client.ObjectKeyFromObject(&secret), &latest) != nil || latest.UID != secret.UID || latest.ResourceVersion != secret.ResourceVersion {
		return errors.New("provider projection changed during renewal")
	}
	before := latest.DeepCopy()
	raw, _ := json.Marshal(grants)
	latest.Data["provider-access"] = raw
	if err := b.Client.Patch(ctx, &latest, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return errors.New("provider access publication failed")
	}
	return errors.Join(failures...)
}

func Run(ctx context.Context, c client.Client, config *rest.Config) error {
	cfg := Config{Control: os.Getenv("PROVIDER_BROKER_CONTROL_NAMESPACE"), Workspace: os.Getenv("PROVIDER_BROKER_WORKSPACE"), UID: os.Getenv("PROVIDER_BROKER_WORKSPACE_UID"), Journal: os.Getenv("PROVIDER_BROKER_JOURNAL"), Owners: map[string]Owner{}}
	if cfg.Control == "" || cfg.Workspace == "" || cfg.UID == "" || cfg.Journal == "" {
		return errors.New("trusted provider broker configuration required")
	}
	for _, name := range []string{"grok", "cursor"} {
		prefix := strings.ToUpper(name) + "_OWNER_"
		owner := Owner{Namespace: os.Getenv(prefix + "NAMESPACE"), Pod: os.Getenv(prefix + "POD"), Container: os.Getenv(prefix + "CONTAINER")}
		if owner.Namespace == "" || owner.Pod == "" || owner.Container == "" {
			return errors.New("explicit credential owner required")
		}
		cfg.Owners[name] = owner
	}
	b := &Broker{Client: c, Config: cfg, Execute: func(ctx context.Context, owner Owner, program string, input []byte) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var output bytes.Buffer
		err := connect.ExecInContainer(ctx, config, owner.Namespace, owner.Pod, owner.Container, []string{"python3", "-c", program}, bytes.NewReader(input), &output, io.Discard, false)
		if err != nil || output.Len() > 128<<10 {
			return nil, errors.New("credential owner unavailable")
		}
		return output.Bytes(), nil
	}}
	for {
		if err := b.Tick(ctx); err != nil {
			slog.Warn("provider broker renewal unavailable", "status", err.Error())
		} else {
			slog.Info("provider broker access leases renewed", "lease_seconds", strconv.Itoa(300))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
}
