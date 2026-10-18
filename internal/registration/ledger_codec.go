package registration

import (
	"fmt"
	"strings"
)

const (
	ledgerPurposeKey = "purpose"
	ledgerPurpose    = "client-registration"
	ownerPrefix      = "owner."
	clientStateKey   = "%s.state"
	clientIDKey      = "%s.id"
	clientPathKey    = "%s.path"
)

// EncodeConfigMapData serializes the ledger into ConfigMap string data.
func EncodeConfigMapData(l *Ledger) map[string]string {
	out := map[string]string{ledgerPurposeKey: ledgerPurpose}
	if l == nil {
		return out
	}
	for kind, rec := range l.Clients {
		out[fmt.Sprintf(clientStateKey, kind)] = rec.State
		if rec.ID != "" {
			out[fmt.Sprintf(clientIDKey, kind)] = rec.ID
		}
		if rec.Path != "" {
			out[fmt.Sprintf(clientPathKey, kind)] = rec.Path
		}
	}
	for worktree, owner := range l.Owners {
		out[ownerPrefix+worktree] = owner
	}
	return out
}

// DecodeConfigMapData loads a ledger from ConfigMap data. Unknown client
// states are preserved so RemovalBlocked fails closed.
func DecodeConfigMapData(data map[string]string) (*Ledger, error) {
	l := NewLedger()
	if len(data) == 0 {
		return l, nil
	}
	if purpose := data[ledgerPurposeKey]; purpose != "" && purpose != ledgerPurpose {
		return nil, fmt.Errorf("unexpected ledger purpose %q", purpose)
	}
	kinds := map[string]bool{}
	for key := range data {
		switch {
		case key == ledgerPurposeKey:
			continue
		case strings.HasPrefix(key, ownerPrefix):
			l.Owners[strings.TrimPrefix(key, ownerPrefix)] = data[key]
		case strings.HasSuffix(key, ".state"):
			kinds[strings.TrimSuffix(key, ".state")] = true
		case strings.HasSuffix(key, ".id"):
			kinds[strings.TrimSuffix(key, ".id")] = true
		case strings.HasSuffix(key, ".path"):
			kinds[strings.TrimSuffix(key, ".path")] = true
		}
	}
	for kind := range kinds {
		rec := ClientRecord{
			Kind:  kind,
			State: data[fmt.Sprintf(clientStateKey, kind)],
			ID:    data[fmt.Sprintf(clientIDKey, kind)],
			Path:  data[fmt.Sprintf(clientPathKey, kind)],
		}
		l.Clients[kind] = rec
	}
	return l, nil
}
