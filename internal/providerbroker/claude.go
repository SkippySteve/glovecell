package providerbroker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Claude's owner holds the subscription's long-lived OAuth token, made once by
// the operator with `claude setup-token` in a temporary login pod and saved
// with its expiry. There is no refresh: the broker only reads it and publishes
// five-minute leases, and the controller hands each cell a stand-in (cell
// token) so the real token stays in the owner, broker, controller and the
// cell's proxy. Renewal is a new sign-in before the saved expiry.
const claudeOwnerRead = `import pathlib,json
p=pathlib.Path.home()/'.claude/apc-subscription-token.json'
assert not p.is_symlink()
s=p.stat();assert s.st_uid==1000 and not s.st_mode&0o077
raw=p.read_bytes();assert len(raw)<16384
v=json.loads(raw)
print(json.dumps({'accessToken':v['accessToken'],'expiresAt':v['expiresAt']}))
`

func (b *Broker) claudeOwner(ctx context.Context) (Access, error) {
	owner, ok := b.Config.Owners["claude"]
	if !ok {
		return Access{}, errors.New("credential owner not configured")
	}
	raw, err := b.Execute(ctx, owner, claudeOwnerRead, []byte("{}"))
	if err != nil || len(raw) > 16<<10 {
		return Access{}, errors.New("credential owner unavailable")
	}
	var value struct {
		Token   string  `json:"accessToken"`
		Expires float64 `json:"expiresAt"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return Access{}, errors.New("invalid Claude owner snapshot")
	}
	access := Access{Token: value.Token, Expires: value.Expires}
	if !strings.HasPrefix(access.Token, "sk-ant-oat01-") || !validAccess(access) {
		return Access{}, errors.New("invalid Claude owner access")
	}
	if access.Expires <= float64(b.now().Unix()) {
		return Access{}, errors.New("Claude owner sign-in required")
	}
	return access, nil
}
