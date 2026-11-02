package providerbroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// These helpers execute only on the selected retained credential owner. Refresh
// credentials exist in the trusted journal only during an interrupted rotation.
const oauthOwnerRead = `import pathlib,json,hashlib,sys
x=json.load(sys.stdin);provider=x['provider']
assert provider in ('cursor','codex')
p=pathlib.Path.home()/('.config/cursor/auth.json' if provider=='cursor' else '.codex/auth.json')
assert not p.is_symlink()
s=p.stat();assert s.st_uid==1000 and not s.st_mode&0o077
raw=p.read_bytes();assert len(raw)<131072
print(json.dumps({'sha256':hashlib.sha256(raw).hexdigest(),'document':json.loads(raw)}))
`
const oauthOwnerWrite = `import pathlib,json,hashlib,os,sys
x=json.load(sys.stdin);provider=x['provider'];assert provider in ('cursor','codex')
p=pathlib.Path.home()/('.config/cursor/auth.json' if provider=='cursor' else '.codex/auth.json')
assert not p.is_symlink();s=p.stat();assert s.st_uid==1000 and not s.st_mode&0o077
raw=p.read_bytes();expected=json.dumps(x['document']).encode();marker=p.with_name('.apc-oauth-commit.json')
committed=False
try:committed=json.loads(raw)==x['document']
except (ValueError,UnicodeError):pass
intent=marker.exists() and json.loads(marker.read_text())==x
assert hashlib.sha256(raw).hexdigest()==x['beforeSHA256'] or committed or intent and expected.startswith(raw),'owner changed'
for proc in pathlib.Path('/proc').iterdir():
 if proc.name.isdigit():
  try: assert (proc/'comm').read_text().strip() not in ('codex','cursor-agent','grok'),'owner busy'
  except (OSError,NotADirectoryError):pass
# Codex owners can use a bind-mounted auth file. Preserve its inode. The saved
# response remains durable until a reread verifies this write after a crash.
if not intent:
 fd=os.open(marker,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
 with os.fdopen(fd,'w') as f:json.dump(x,f);f.flush();os.fsync(f.fileno())
 directory=os.open(p.parent,os.O_RDONLY);os.fsync(directory);os.close(directory)
fd=os.open(p,os.O_WRONLY|os.O_TRUNC|os.O_NOFOLLOW)
with os.fdopen(fd,'w') as f:json.dump(x['document'],f);f.flush();os.fsync(f.fileno())
assert json.loads(p.read_text())==x['document']
marker.unlink(missing_ok=True)
print('saved')
`

type oauthSnapshot struct {
	SHA      string         `json:"sha256"`
	Document map[string]any `json:"document"`
}
type oauthRenewal struct {
	Stage    string         `json:"stage"`
	Provider string         `json:"provider"`
	Before   string         `json:"beforeSHA256"`
	Document map[string]any `json:"document"`
}

type tokenClaims struct {
	Exp  float64 `json:"exp"`
	Sub  string  `json:"sub"`
	Auth struct {
		Account string `json:"chatgpt_account_id"`
		Plan    string `json:"chatgpt_plan_type"`
	} `json:"https://api.openai.com/auth"`
}

func claims(token string) (tokenClaims, error) {
	parts := strings.Split(token, "::")
	parts = strings.Split(parts[len(parts)-1], ".")
	if len(parts) != 3 {
		return tokenClaims{}, errors.New("invalid owner token")
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[1])
	var value tokenClaims
	if e != nil || json.Unmarshal(raw, &value) != nil || value.Exp <= 0 {
		return tokenClaims{}, errors.New("invalid owner claims")
	}
	return value, nil
}
func parseOwner(provider string, document map[string]any) (Access, error) {
	if provider == "cursor" {
		token, _ := document["accessToken"].(string)
		c, e := claims(token)
		if e != nil || c.Sub == "" {
			return Access{}, errors.New("invalid Cursor owner")
		}
		return Access{Token: token, Expires: c.Exp, Subject: c.Sub}, nil
	}
	tokens, _ := document["tokens"].(map[string]any)
	token, _ := tokens["access_token"].(string)
	c, e := claims(token)
	account, _ := tokens["account_id"].(string)
	if account == "" {
		account = c.Auth.Account
	}
	if e != nil || account == "" || c.Auth.Account != "" && c.Auth.Account != account {
		return Access{}, errors.New("invalid Codex owner")
	}
	return Access{Token: token, Expires: c.Exp, Account: account, Plan: c.Auth.Plan}, nil
}

func (b *Broker) oauthOwner(ctx context.Context, provider string) (Access, error) {
	owner, ok := b.Config.Owners[provider]
	if !ok {
		return Access{}, errors.New("credential owner not configured")
	}
	var journal corev1.Secret
	if b.Client.Get(ctx, types.NamespacedName{Namespace: b.Config.Control, Name: b.Config.Journal}, &journal) != nil || !b.journalBound(&journal) {
		return Access{}, errors.New("renewal journal binding unavailable")
	}
	key := provider + "-renewal"
	var pending oauthRenewal
	if raw := journal.Data[key]; len(raw) > 0 {
		if json.Unmarshal(raw, &pending) != nil || pending.Stage != "response-saved" || pending.Provider != provider {
			return Access{}, errors.New("interrupted OAuth request requires owner recovery")
		}
	} else {
		input, _ := json.Marshal(map[string]string{"provider": provider})
		raw, e := b.Execute(ctx, owner, oauthOwnerRead, input)
		if e != nil || len(raw) > 128<<10 {
			return Access{}, errors.New("credential owner unavailable")
		}
		var current oauthSnapshot
		if json.Unmarshal(raw, &current) != nil || current.SHA == "" || current.Document == nil {
			return Access{}, errors.New("invalid owner snapshot")
		}
		access, e := parseOwner(provider, current.Document)
		if e != nil {
			return Access{}, e
		}
		if !b.Config.ForceRenew[provider] && access.Expires > float64(b.now().Add(10*time.Minute).Unix()) {
			return access, nil
		}
		refresh := ""
		if provider == "cursor" {
			refresh, _ = current.Document["refreshToken"].(string)
		} else {
			refresh, _ = current.Document["tokens"].(map[string]any)["refresh_token"].(string)
		}
		id := b.Config.OAuthClientIDs[provider]
		if refresh == "" || id == "" {
			return Access{}, errors.New("reviewed OAuth client and owner sign-in required")
		}
		pending = oauthRenewal{Stage: "request-started", Provider: provider, Before: current.SHA, Document: current.Document}
		if e = b.saveOAuth(ctx, &journal, key, pending); e != nil {
			return Access{}, e
		}
		result, e := b.refreshOAuth(ctx, provider, refresh, id)
		if e != nil {
			return Access{}, e
		}
		if provider == "cursor" {
			current.Document["accessToken"] = result.Token
			if result.Refresh != "" {
				current.Document["refreshToken"] = result.Refresh
			} else {
				current.Document["refreshToken"] = result.Token
			}
		} else {
			tokens := current.Document["tokens"].(map[string]any)
			tokens["access_token"] = result.Token
			if result.Refresh != "" {
				tokens["refresh_token"] = result.Refresh
			}
			if result.IDToken != "" {
				tokens["id_token"] = result.IDToken
			}
			current.Document["last_refresh"] = b.now().UTC().Format(time.RFC3339Nano)
		}
		renewed, e := parseOwner(provider, current.Document)
		if e != nil || !validAccess(renewed) || renewed.Expires <= float64(b.now().Add(time.Minute).Unix()) || provider == "codex" && renewed.Account != access.Account || provider == "cursor" && renewed.Subject != access.Subject {
			return Access{}, errors.New("renewal changed account or returned invalid access")
		}
		pending.Stage = "response-saved"
		if e = b.saveOAuth(ctx, &journal, key, pending); e != nil {
			return Access{}, e
		}
	}
	input, _ := json.Marshal(pending)
	if _, e := b.Execute(ctx, owner, oauthOwnerWrite, input); e != nil {
		return Access{}, errors.New("owner renewal commit pending")
	}
	// Verify persisted owner bytes before discarding the durable response.
	input, _ = json.Marshal(map[string]string{"provider": provider})
	raw, e := b.Execute(ctx, owner, oauthOwnerRead, input)
	var observed oauthSnapshot
	if e != nil || json.Unmarshal(raw, &observed) != nil {
		return Access{}, errors.New("owner renewal verification pending")
	}
	expected, _ := json.Marshal(pending.Document)
	actual, _ := json.Marshal(observed.Document)
	if !bytes.Equal(expected, actual) {
		return Access{}, errors.New("owner renewal verification pending")
	}
	access, e := parseOwner(provider, pending.Document)
	if e != nil {
		return Access{}, e
	}
	before := journal.DeepCopy()
	delete(journal.Data, key)
	if b.Client.Patch(ctx, &journal, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})) != nil {
		return Access{}, errors.New("owner renewal receipt pending")
	}
	return access, nil
}
func (b *Broker) saveOAuth(ctx context.Context, journal *corev1.Secret, key string, value oauthRenewal) error {
	before := journal.DeepCopy()
	if journal.Data == nil {
		journal.Data = map[string][]byte{}
	}
	journal.Data[key], _ = json.Marshal(value)
	if b.Client.Patch(ctx, journal, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})) != nil {
		return errors.New("OAuth journal update failed")
	}
	return nil
}
func (b *Broker) refreshOAuth(ctx context.Context, provider, refresh, id string) (oauthResponse, error) {
	endpoint := "https://auth.openai.com/oauth/token"
	if provider == "cursor" {
		endpoint = "https://api2.cursor.sh/oauth/token"
	} else if provider != "codex" {
		return oauthResponse{}, errors.New("unsupported owner")
	}
	raw, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refresh, "client_id": id})
	req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: 25 * time.Second}
	if b.HTTP != nil {
		*httpClient = *b.HTTP
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("credential redirects denied") }
	res, e := httpClient.Do(req)
	if e != nil {
		return oauthResponse{}, errors.New("owner OAuth renewal unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return oauthResponse{}, errors.New("owner reauthorization required")
	}
	body, e := io.ReadAll(io.LimitReader(res.Body, 128<<10+1))
	var result oauthResponse
	if e != nil || len(body) > 128<<10 || json.Unmarshal(body, &result) != nil || result.Logout || result.Token == "" {
		return oauthResponse{}, errors.New("invalid OAuth response")
	}
	return result, nil
}
