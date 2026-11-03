package proxy

import (
	"context"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/credentials"
)

// The credentials the proxy injects, named as credential URIs
// (internal/credentials). Today they come from the proxy's own Secret volume,
// /tls, which only the proxy mounts; a backend such as OpenBao can serve the
// same names later without changing the injection code.
const (
	CellCredentialClass    = "cell.agentwork.dev"
	GitHubTokenURI         = credentials.Scheme + "://" + CellCredentialClass + "/tls/github-token"
	MuseSubscriptionKeyURI = credentials.Scheme + "://" + CellCredentialClass + "/tls/muse-subscription-key"
)

func gatewayCredentials() credentials.Provider {
	return credentials.Files{
		Class:   CellCredentialClass,
		Volumes: map[string]string{"tls": "/tls"},
		Keys:    []string{"tls/github-token", "tls/muse-subscription-key"},
	}
}

// credential fetches one secret and checks it again before it goes into a
// header, whatever provider served it. No provider fails closed.
func (s *Server) credential(ctx context.Context, uri string) ([]byte, error) {
	if s.Credentials == nil {
		return nil, credentials.ErrUnavailable
	}
	secret, err := s.Credentials.FetchSecret(ctx, credentials.Request{URI: uri})
	if err != nil {
		return nil, err
	}
	if err := credentials.Usable(secret); err != nil {
		return nil, err
	}
	return secret, nil
}
