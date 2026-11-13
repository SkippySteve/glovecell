// Package hubadmin exposes a narrow, mutually authenticated control path to an
// Orca phone hub. It never accepts shell commands or arbitrary CDP expressions.
package hubadmin

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("hub administration unavailable")
var DeviceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Offer struct {
	Available  bool   `json:"available"`
	Endpoint   string `json:"endpoint"`
	PairingURL string `json:"pairingUrl,omitempty"`
	QRDataURL  string `json:"qrDataUrl,omitempty"`
	DeviceID   string `json:"deviceId,omitempty"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
}
type Device struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	PairedAt int64  `json:"pairedAt"`
}
type Devices struct {
	Devices []Device `json:"devices"`
}
type Backend interface {
	Pair(context.Context) (Offer, error)
	Devices(context.Context) (Devices, error)
	Revoke(context.Context, string) (bool, error)
}

func ValidateEndpoint(endpoint string) error {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "wss" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("hub public endpoint must be an explicit WSS origin")
	}
	return nil
}
func ValidateOffer(o Offer, endpoint string, now time.Time) error {
	if !o.Available {
		if o.PairingURL != "" || o.QRDataURL != "" {
			return ErrUnavailable
		}
		return nil
	}
	u, e := url.Parse(o.PairingURL)
	expiry := time.UnixMilli(o.ExpiresAt)
	if e != nil || u.Scheme != "orca" || u.Host != "pair" || o.Endpoint != endpoint || !DeviceID.MatchString(o.DeviceID) || !expiry.After(now) || expiry.After(now.Add(10*time.Minute+30*time.Second)) || len(o.PairingURL) > 32<<10 || len(o.QRDataURL) > 128<<10 || o.QRDataURL != "" && !strings.HasPrefix(o.QRDataURL, "data:image/png;base64,") {
		return ErrUnavailable
	}
	return nil
}
func decode(reader io.Reader, target any) error {
	d := json.NewDecoder(io.LimitReader(reader, 256<<10))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return ErrUnavailable
	}
	return nil
}
func jsonReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Handler is reachable only after mTLS; the controller-facing API adds OIDC,
// Origin checks and audit attribution. No credential-bearing errors are sent.
func Handler(backend Backend) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		v, e := backend.Devices(r.Context())
		if e != nil {
			http.Error(w, ErrUnavailable.Error(), 503)
			return
		}
		jsonReply(w, v)
	})
	mux.HandleFunc("POST /v1/pairing", func(w http.ResponseWriter, r *http.Request) {
		v, e := backend.Pair(r.Context())
		if e != nil {
			http.Error(w, ErrUnavailable.Error(), 503)
			return
		}
		jsonReply(w, v)
	})
	mux.HandleFunc("DELETE /v1/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !DeviceID.MatchString(id) {
			http.Error(w, "invalid device", 400)
			return
		}
		ok, e := backend.Revoke(r.Context(), id)
		if e != nil || !ok {
			http.Error(w, "device revocation unavailable", 409)
			return
		}
		jsonReply(w, map[string]bool{"revoked": true})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "mutual TLS required", 403)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

type TLSFiles struct{ CA, Cert, Key string }

func (f TLSFiles) roots() (*x509.CertPool, error) {
	raw, e := os.ReadFile(f.CA)
	if e != nil {
		return nil, ErrUnavailable
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, ErrUnavailable
	}
	return pool, nil
}
func (f TLSFiles) certificate() (*tls.Certificate, error) {
	c, e := tls.LoadX509KeyPair(f.Cert, f.Key)
	if e != nil {
		return nil, ErrUnavailable
	}
	return &c, nil
}
func ServerTLS(files TLSFiles, clientURI string) (*tls.Config, error) {
	u, e := url.Parse(clientURI)
	if e != nil || u.Scheme != "spiffe" || u.Host == "" || u.Path == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("explicit controller SPIFFE URI required")
	}
	build := func() (*tls.Config, error) {
		roots, e := files.roots()
		if e != nil {
			return nil, e
		}
		cert, e := files.certificate()
		if e != nil {
			return nil, e
		}
		return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{*cert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, VerifyConnection: func(c tls.ConnectionState) error {
			if len(c.VerifiedChains) == 0 || len(c.PeerCertificates) == 0 {
				return ErrUnavailable
			}
			for _, v := range c.PeerCertificates[0].URIs {
				if v.String() == clientURI {
					return nil
				}
			}
			return ErrUnavailable
		}}, nil
	}
	cfg, e := build()
	if e != nil {
		return nil, e
	}
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) { return build() }
	return cfg, nil
}

type Client struct {
	URL, Endpoint string
	Files         TLSFiles
}

func NewClient(origin, endpoint string, files TLSFiles) (*Client, error) {
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, errors.New("hub admin requires explicit HTTPS origin")
	}
	if e = ValidateEndpoint(endpoint); e != nil {
		return nil, e
	}
	if _, e = files.roots(); e != nil {
		return nil, e
	}
	if _, e = files.certificate(); e != nil {
		return nil, e
	}
	return &Client{URL: strings.TrimSuffix(origin, "/"), Endpoint: endpoint, Files: files}, nil
}
func (c *Client) call(ctx context.Context, method, path string, target any) error {
	roots, e := c.Files.roots()
	if e != nil {
		return e
	}
	cert, e := c.Files.certificate()
	if e != nil {
		return e
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{*cert}}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrUnavailable }}
	request, e := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(nil))
	if e != nil {
		return ErrUnavailable
	}
	response, e := client.Do(request)
	if e != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return ErrUnavailable
	}
	return decode(response.Body, target)
}
func (c *Client) Pair(ctx context.Context) (Offer, error) {
	var o Offer
	e := c.call(ctx, "POST", "/v1/pairing", &o)
	if e == nil {
		e = ValidateOffer(o, c.Endpoint, time.Now())
	}
	if e != nil {
		return Offer{}, ErrUnavailable
	}
	return o, nil
}
func (c *Client) Devices(ctx context.Context) (Devices, error) {
	var d Devices
	e := c.call(ctx, "GET", "/v1/devices", &d)
	if e == nil {
		e = ValidateDevices(d)
	}
	if e != nil {
		return Devices{}, ErrUnavailable
	}
	return d, nil
}
func (c *Client) Revoke(ctx context.Context, id string) (bool, error) {
	if !DeviceID.MatchString(id) {
		return false, ErrUnavailable
	}
	var reply struct {
		Revoked bool `json:"revoked"`
	}
	e := c.call(ctx, "DELETE", "/v1/devices/"+id, &reply)
	return reply.Revoked, e
}
func ValidateDevices(d Devices) error {
	if len(d.Devices) > 256 {
		return ErrUnavailable
	}
	seen := map[string]bool{}
	for _, v := range d.Devices {
		if !DeviceID.MatchString(v.DeviceID) || len(v.Name) > 256 || seen[v.DeviceID] {
			return ErrUnavailable
		}
		seen[v.DeviceID] = true
	}
	return nil
}
