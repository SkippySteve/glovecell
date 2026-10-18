package providerbroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fixture(t *testing.T) (*Broker, *api.ProjectWorkspace, *corev1.Secret, map[string]map[string]any) {
	t.Helper()
	uid := "12345678-1234-1234-1234-123456789abc"
	now := time.Now().UTC()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	work := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "workbench", Namespace: "control", UID: types.UID(uid)}, Spec: api.WorkspaceSpec{Repository: "owner/project", DesiredState: "Running"}}
	grants := map[string]Grant{"grok": {Enabled: true, Access: &Access{Token: "old-access", Expires: float64(now.Add(time.Hour).Unix()), Issuer: "https://auth.x.ai"}}, "cursor": {Enabled: true, Access: &Access{Token: jwtFixture("cursor-account", now.Add(30*time.Minute)), Expires: float64(now.Add(time.Hour).Unix())}}, "muse": {Enabled: false}}
	raw, _ := json.Marshal(grants)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "model-access", Namespace: "apc-" + strings.ReplaceAll(uid, "-", ""), UID: "model-uid", Labels: map[string]string{api.OwnerLabel: uid}}, Data: map[string][]byte{"provider-access": raw, "access-token": []byte("untouched-codex-access")}}
	journal := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "journal", Namespace: "control", Labels: map[string]string{"providers.agentwork.dev/workspace-uid": uid}}, Data: map[string][]byte{}}
	owner := map[string]map[string]any{"https://auth.x.ai::fixture-client": {"oidc_issuer": "https://auth.x.ai", "oidc_client_id": "fixture-client", "key": "new-grok-access", "refresh_token": "never-deliver-refresh", "expires_at": now.Add(time.Hour).Format(time.RFC3339Nano)}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(work, secret, journal).Build()
	claims, _ := json.Marshal(map[string]any{"exp": now.Add(time.Hour).Unix(), "sub": "cursor-account"})
	token := "owner::header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	b := &Broker{Client: c, Config: Config{Control: "control", Workspace: "workbench", UID: uid, Journal: "journal", Owners: map[string]Owner{"grok": {}, "cursor": {}}}, Now: func() time.Time { return now }, Execute: func(_ context.Context, _ Owner, program string, input []byte) ([]byte, error) {
		if program == ownerWrite || program == oauthOwnerWrite {
			return []byte("saved"), nil
		}
		var args map[string]string
		_ = json.Unmarshal(input, &args)
		if args["provider"] == "cursor" {
			return json.Marshal(map[string]any{"sha256": "fixture-sha", "document": map[string]string{"accessToken": token, "refreshToken": "private-refresh"}})
		}
		return json.Marshal(snapshot{SHA: "fixture-sha", Document: owner})
	}}
	return b, work, secret, owner
}

func read(t *testing.T, b *Broker, secret *corev1.Secret) map[string]Grant {
	t.Helper()
	var latest corev1.Secret
	if err := b.Client.Get(context.Background(), client.ObjectKeyFromObject(secret), &latest); err != nil {
		t.Fatal(err)
	}
	if string(latest.Data["access-token"]) != "untouched-codex-access" {
		t.Fatal("replaced another provider")
	}
	if strings.Contains(string(latest.Data["provider-access"]), "refresh") {
		t.Fatal("refresh material entered worker projection")
	}
	var grants map[string]Grant
	if json.Unmarshal(latest.Data["provider-access"], &grants) != nil {
		t.Fatal("invalid publication")
	}
	return grants
}

func TestRenewalBoundLeaseAndDurableRevocation(t *testing.T) {
	b, _, secret, _ := fixture(t)
	ctx := context.Background()
	if err := b.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	grants := read(t, b, secret)
	if grants["grok"].Access.Token != "new-grok-access" || grants["grok"].Access.Expires != float64(b.now().Add(5*time.Minute).Unix()) {
		t.Fatal("renewal did not issue bounded access lease")
	}
	var current corev1.Secret
	_ = b.Client.Get(ctx, client.ObjectKeyFromObject(secret), &current)
	grants["grok"] = Grant{Enabled: false}
	current.Data["provider-access"], _ = json.Marshal(grants)
	if err := b.Client.Update(ctx, &current); err != nil {
		t.Fatal(err)
	}
	restarted := *b
	if err := restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if read(t, b, secret)["grok"].Enabled {
		t.Fatal("restart undid revocation")
	}
}

func TestMalformedEnrollmentNeverReadsOwnersOrPublishes(t *testing.T) {
	for _, raw := range []string{
		`null`,
		`{"unknown":{"enabled":true}}`,
		`{"grok":{"enabled":true,"refresh_token":"private"}}`,
		`{"cursor":{"enabled":true,"access":{"accessToken":"token","expiresAt":123,"refreshToken":"private"}}}`,
		`{"cursor":{"enabled":false,"access":{"accessToken":"token","expiresAt":123}}}`,
		`{"muse":{"enabled":true,"access":{"accessToken":"actual-subscription-key","expiresAt":123}}}`,
		`{} {}`,
	} {
		t.Run(raw, func(t *testing.T) {
			b, _, secret, _ := fixture(t)
			secret.Data["provider-access"] = []byte(raw)
			if err := b.Client.Update(context.Background(), secret); err != nil {
				t.Fatal(err)
			}
			b.Execute = func(context.Context, Owner, string, []byte) ([]byte, error) {
				t.Fatal("malformed enrollment read credential owner")
				return nil, nil
			}
			if b.Tick(context.Background()) == nil {
				t.Fatal("malformed enrollment accepted")
			}
			var unchanged corev1.Secret
			if err := b.Client.Get(context.Background(), client.ObjectKeyFromObject(secret), &unchanged); err != nil {
				t.Fatal(err)
			}
			if string(unchanged.Data["provider-access"]) != raw {
				t.Fatal("malformed enrollment overwritten")
			}
		})
	}
}

func TestRemovedOrReplacedWorkbenchDoesNotReadOwners(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		b, w, _, _ := fixture(t)
		ctx := context.Background()
		if replaced {
			w.UID = "other"
		} else {
			w.Spec.DesiredState = "Removed"
		}
		if err := b.Client.Update(ctx, w); err != nil {
			t.Fatal(err)
		}
		b.Execute = func(context.Context, Owner, string, []byte) ([]byte, error) {
			t.Fatal("read credentials for removed/replaced workbench")
			return nil, nil
		}
		if b.Tick(ctx) == nil {
			t.Fatal("accepted removed/replaced identity")
		}
	}
}

func TestOneOwnerOutageDoesNotBlockOtherProviderAndNeverReplaysTasks(t *testing.T) {
	b, _, secret, _ := fixture(t)
	original := b.Execute
	b.Execute = func(ctx context.Context, o Owner, program string, input []byte) ([]byte, error) {
		if program != ownerRead && program != ownerWrite && program != oauthOwnerRead && program != oauthOwnerWrite {
			t.Fatal("unexpected owner command")
		}
		if strings.Contains(string(input), "grok") {
			return nil, errors.New("owner offline")
		}
		return original(ctx, o, program, input)
	}
	if b.Tick(context.Background()) == nil {
		t.Fatal("outage not reported")
	}
	grants := read(t, b, secret)
	if grants["cursor"].Access.Token == jwtFixture("cursor-account", b.now().Add(30*time.Minute)) || grants["grok"].Access.Token != "old-access" {
		t.Fatal("outage affected another provider")
	}
}

type roundtrip func(*http.Request) (*http.Response, error)

func (f roundtrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOAuthRotationJournalRecoversOwnerCommitWithoutReplayingRefresh(t *testing.T) {
	b, _, secret, owner := fixture(t)
	for _, v := range owner {
		v["expires_at"] = b.now().Add(time.Minute).Format(time.RFC3339Nano)
	}
	requests := 0
	b.HTTP = &http.Client{Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.String() != "https://auth.x.ai/oauth2/token" || r.Method != "POST" {
			t.Fatal("credential destination changed")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"rotated-access","refresh_token":"rotated-private-refresh","expires_in":3600}`)), Request: r}, nil
	})}
	original := b.Execute
	fail := true
	b.Execute = func(ctx context.Context, o Owner, program string, input []byte) ([]byte, error) {
		if program == ownerWrite && fail {
			return nil, errors.New("interrupted owner commit")
		}
		return original(ctx, o, program, input)
	}
	if b.Tick(context.Background()) == nil {
		t.Fatal("interruption not reported")
	}
	fail = false
	restarted := *b
	if err := restarted.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || read(t, b, secret)["grok"].Access.Token != "rotated-access" {
		t.Fatal("refresh replayed or response recovery failed")
	}
	var journal corev1.Secret
	_ = b.Client.Get(context.Background(), types.NamespacedName{Namespace: "control", Name: "journal"}, &journal)
	if len(journal.Data["grok-renewal"]) != 0 {
		t.Fatal("completed credential transaction retained refresh copy")
	}
}

func TestCredentialRedirectNeverReachesAnotherOrigin(t *testing.T) {
	b, _, _, _ := fixture(t)
	calls := 0
	b.HTTP = &http.Client{Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": {"https://other.example/token"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	if _, err := b.refresh(context.Background(), "private-refresh", "fixture-client"); err == nil || calls != 1 {
		t.Fatal("credential followed redirect")
	}
}

func TestUncertainOAuthOutcomeRequiresOwnerRecovery(t *testing.T) {
	b, _, secret, owner := fixture(t)
	for _, value := range owner {
		value["expires_at"] = b.now().Add(time.Minute).Format(time.RFC3339Nano)
	}
	requests := 0
	b.HTTP = &http.Client{Transport: roundtrip(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("interrupted response")
	})}
	if b.Tick(context.Background()) == nil {
		t.Fatal("interrupted request not reported")
	}
	restarted := *b
	if restarted.Tick(context.Background()) == nil {
		t.Fatal("uncertain OAuth outcome silently retried")
	}
	if requests != 1 || read(t, b, secret)["grok"].Access.Token != "old-access" {
		t.Fatal("uncertain refresh replayed or published")
	}
}

func TestRevocationDuringOwnerReadCannotBeUndone(t *testing.T) {
	b, _, secret, _ := fixture(t)
	original := b.Execute
	revoked := false
	b.Execute = func(ctx context.Context, owner Owner, program string, input []byte) ([]byte, error) {
		if !revoked {
			revoked = true
			var current corev1.Secret
			if err := b.Client.Get(ctx, client.ObjectKeyFromObject(secret), &current); err != nil {
				t.Fatal(err)
			}
			grants := read(t, b, secret)
			grants["grok"] = Grant{Enabled: false}
			current.Data["provider-access"], _ = json.Marshal(grants)
			if err := b.Client.Update(ctx, &current); err != nil {
				t.Fatal(err)
			}
		}
		return original(ctx, owner, program, input)
	}
	if b.Tick(context.Background()) == nil {
		t.Fatal("changed projection overwritten")
	}
	if read(t, b, secret)["grok"].Enabled {
		t.Fatal("concurrent revocation undone")
	}
}

func jwtFixture(account string, expiry time.Time) string {
	raw, _ := json.Marshal(map[string]any{"exp": expiry.Unix(), "sub": account, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account, "chatgpt_plan_type": "pro"}})
	return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
}
func TestCodexAndCursorOAuthCommitRecoveryAndAccountBinding(t *testing.T) {
	for _, provider := range []string{"codex", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			b, _, secret, _ := fixture(t)
			ctx := context.Background()
			b.Config.Owners[provider] = Owner{}
			b.Config.OAuthClientIDs = map[string]string{provider: "reviewed-public-client"}
			old := jwtFixture("same-account", b.now().Add(time.Minute))
			fresh := jwtFixture("same-account", b.now().Add(time.Hour))
			document := map[string]any{"accessToken": old, "refreshToken": "private-refresh"}
			if provider == "codex" {
				document = map[string]any{"tokens": map[string]any{"access_token": old, "refresh_token": "private-refresh", "account_id": "same-account"}}
			}
			var grants map[string]Grant
			_ = json.Unmarshal(secret.Data["provider-access"], &grants)
			grants[provider] = Grant{Enabled: true, Access: &Access{Token: old, Expires: float64(b.now().Add(time.Minute).Unix()), Account: func() string {
				if provider == "codex" {
					return "same-account"
				}
				return ""
			}()}}
			secret.Data["provider-access"], _ = json.Marshal(grants)
			if err := b.Client.Update(ctx, secret); err != nil {
				t.Fatal(err)
			}
			original := b.Execute
			writes := 0
			interrupt := true
			b.Execute = func(ctx context.Context, o Owner, program string, input []byte) ([]byte, error) {
				var args map[string]any
				_ = json.Unmarshal(input, &args)
				if args["provider"] != provider {
					return original(ctx, o, program, input)
				}
				if program == oauthOwnerRead {
					return json.Marshal(oauthSnapshot{SHA: "snapshot-sha", Document: document})
				}
				if program == oauthOwnerWrite {
					writes++
					if interrupt {
						return nil, errors.New("interrupted write")
					}
					document = args["document"].(map[string]any)
					return []byte("saved"), nil
				}
				t.Fatal("unexpected program")
				return nil, nil
			}
			requests := 0
			b.HTTP = &http.Client{Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
				requests++
				endpoint := "https://auth.openai.com/oauth/token"
				if provider == "cursor" {
					endpoint = "https://api2.cursor.sh/oauth/token"
				}
				if r.URL.String() != endpoint || r.Method != "POST" {
					t.Fatal("unreviewed endpoint")
				}
				raw, _ := json.Marshal(map[string]any{"access_token": fresh, "refresh_token": "rotated-private-refresh", "expires_in": 3600})
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw))), Request: r}, nil
			})}
			if b.Tick(ctx) == nil {
				t.Fatal("interruption not reported")
			}
			interrupt = false
			restarted := *b
			if err := restarted.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			var latest corev1.Secret
			_ = b.Client.Get(ctx, client.ObjectKeyFromObject(secret), &latest)
			_ = json.Unmarshal(latest.Data["provider-access"], &grants)
			if requests != 1 || writes != 2 || grants[provider].Access.Token != fresh || strings.Contains(string(latest.Data["provider-access"]), "private-refresh") {
				t.Fatal("recovery replayed refresh or exposed credentials")
			}
			var journal corev1.Secret
			_ = b.Client.Get(ctx, types.NamespacedName{Namespace: "control", Name: "journal"}, &journal)
			if len(journal.Data[provider+"-renewal"]) != 0 {
				t.Fatal("completed rotation retained refresh copy")
			}
		})
	}
}
func TestOAuthDeniedAccountChangeAndUncertainResponseNeverReplay(t *testing.T) {
	for _, provider := range []string{"codex", "cursor"} {
		for _, outcome := range []string{"substitution", "interrupted", "redirect", "logout"} {
			t.Run(provider+"/"+outcome, func(t *testing.T) {
				b, _, _, _ := fixture(t)
				b.Config.Owners[provider] = Owner{}
				b.Config.OAuthClientIDs = map[string]string{provider: "reviewed-client"}
				old := jwtFixture("original", b.now().Add(time.Minute))
				document := map[string]any{"accessToken": old, "refreshToken": "private-refresh"}
				if provider == "codex" {
					document = map[string]any{"tokens": map[string]any{"access_token": old, "refresh_token": "private-refresh", "account_id": "original"}}
				}
				b.Execute = func(_ context.Context, _ Owner, program string, _ []byte) ([]byte, error) {
					if program != oauthOwnerRead {
						t.Fatal("denied response reached owner write")
					}
					return json.Marshal(oauthSnapshot{SHA: "sha", Document: document})
				}
				requests := 0
				b.HTTP = &http.Client{Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
					requests++
					if outcome == "interrupted" {
						return nil, errors.New("response lost")
					}
					status := 200
					header := make(http.Header)
					if outcome == "redirect" {
						status = 307
						header.Set("Location", "https://other.example/token")
					}
					raw, _ := json.Marshal(map[string]any{"access_token": jwtFixture("different", b.now().Add(time.Hour)), "shouldLogout": outcome == "logout"})
					return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(string(raw))), Request: r}, nil
				})}
				if _, err := b.oauthOwner(context.Background(), provider); err == nil {
					t.Fatal("unsafe response accepted")
				}
				restarted := *b
				if _, err := restarted.oauthOwner(context.Background(), provider); err == nil {
					t.Fatal("uncertain request replayed")
				}
				if requests != 1 {
					t.Fatal("duplicate OAuth request")
				}
			})
		}
	}
}
