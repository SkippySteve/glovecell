package hubadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Runtime talks only to the hub's loopback Chrome DevTools instance. The CDP
// expressions below are fixed operations; no client supplies JavaScript.
type Runtime struct {
	Endpoint, DevTools string
	mu                 sync.Mutex
}

func (r *Runtime) evaluate(ctx context.Context, expression string, target any) error {
	origin := r.DevTools
	if origin == "" {
		origin = "http://127.0.0.1:9222"
	}
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return ErrUnavailable
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrUnavailable }}
	request, _ := http.NewRequestWithContext(ctx, "GET", origin+"/json/list", nil)
	response, e := client.Do(request)
	if e != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return ErrUnavailable
	}
	var targets []struct {
		Type string `json:"type"`
		URL  string `json:"webSocketDebuggerUrl"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&targets) != nil {
		return ErrUnavailable
	}
	endpoint := ""
	for _, v := range targets {
		if v.Type == "page" {
			endpoint = v.URL
			break
		}
	}
	ws, e := url.Parse(endpoint)
	if e != nil || ws.Scheme != "ws" || ws.Host != u.Host || ws.User != nil || ws.RawQuery != "" || ws.Fragment != "" {
		return ErrUnavailable
	}
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second, Proxy: nil}
	connection, _, e := dialer.DialContext(ctx, endpoint, nil)
	if e != nil {
		return ErrUnavailable
	}
	defer connection.Close()
	connection.SetReadLimit(256 << 10)
	deadline := time.Now().Add(7 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = connection.SetReadDeadline(deadline)
	_ = connection.SetWriteDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	message := map[string]any{"id": 1, "method": "Runtime.evaluate", "params": map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true}}
	if connection.WriteJSON(message) != nil {
		return ErrUnavailable
	}
	for {
		var reply struct {
			ID     int             `json:"id"`
			Error  json.RawMessage `json:"error"`
			Result struct {
				Result struct {
					Value json.RawMessage `json:"value"`
				} `json:"result"`
				Exception json.RawMessage `json:"exceptionDetails"`
			} `json:"result"`
		}
		if connection.ReadJSON(&reply) != nil {
			return ErrUnavailable
		}
		if reply.ID != 1 {
			continue
		}
		if len(reply.Error) > 0 || len(reply.Result.Exception) > 0 || len(reply.Result.Result.Value) == 0 {
			return ErrUnavailable
		}
		if json.Unmarshal(reply.Result.Result.Value, target) != nil {
			return ErrUnavailable
		}
		return nil
	}
}
func (r *Runtime) Pair(ctx context.Context) (Offer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ValidateEndpoint(r.Endpoint) != nil {
		return Offer{}, ErrUnavailable
	}
	address, _ := json.Marshal(r.Endpoint)
	expr := `(async()=>{const v=await window.api.mobile.getPairingQR({address:` + string(address) + `,rotate:true,connectionMode:'local-only'});return v.available?{available:true,endpoint:v.endpoint,pairingUrl:v.pairingUrl,qrDataUrl:v.qrDataUrl||'',deviceId:v.deviceId,expiresAt:v.expiresAt}:{available:false,endpoint:` + string(address) + `}})()`
	var offer Offer
	if r.evaluate(ctx, expr, &offer) != nil || ValidateOffer(offer, r.Endpoint, time.Now()) != nil {
		return Offer{}, ErrUnavailable
	}
	return offer, nil
}
func (r *Runtime) Devices(ctx context.Context) (Devices, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var d Devices
	expr := `(async()=>{const v=await window.api.mobile.listDevices();return {devices:v.devices.map(({deviceId,name,pairedAt})=>({deviceId,name,pairedAt}))}})()`
	if r.evaluate(ctx, expr, &d) != nil || ValidateDevices(d) != nil {
		return Devices{}, ErrUnavailable
	}
	return d, nil
}
func (r *Runtime) Revoke(ctx context.Context, id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !DeviceID.MatchString(id) {
		return false, ErrUnavailable
	}
	encoded, _ := json.Marshal(id)
	var reply struct {
		Revoked bool `json:"revoked"`
	}
	if r.evaluate(ctx, `window.api.mobile.revokeDevice({deviceId:`+string(encoded)+`}).then(v=>({revoked:v.revoked===true}))`, &reply) != nil {
		return false, ErrUnavailable
	}
	return reply.Revoked, nil
}

// Run serves a fixed-operation private admin listener. Its TLS and network
// isolation are deployment prerequisites; it is never a public console route.
func Run(ctx context.Context) error {
	endpoint := os.Getenv("HUB_PUBLIC_ADDRESS")
	if e := ValidateEndpoint(endpoint); e != nil {
		return e
	}
	files := TLSFiles{CA: os.Getenv("HUB_ADMIN_CLIENT_CA_FILE"), Cert: os.Getenv("HUB_ADMIN_CERT_FILE"), Key: os.Getenv("HUB_ADMIN_KEY_FILE")}
	config, e := ServerTLS(files, os.Getenv("HUB_ADMIN_CLIENT_URI"))
	if e != nil {
		return e
	}
	listener := os.Getenv("HUB_ADMIN_LISTEN")
	if listener == "" {
		listener = ":16769"
	}
	server := &http.Server{Addr: listener, Handler: Handler(&Runtime{Endpoint: endpoint}), TLSConfig: config, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	e = server.ListenAndServeTLS("", "")
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	if e != nil {
		return fmt.Errorf("hub administration listener stopped")
	}
	return nil
}
