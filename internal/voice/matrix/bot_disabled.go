//go:build !goolm

package matrix

import (
	"context"
	"database/sql"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/speech"
)

// Enabled reports whether this build has the E2EE Matrix client.
const Enabled = false

// Bot is unavailable without the goolm build tag.
type Bot struct{}

// New fails closed: a bot without E2EE could not tell verified devices apart.
func New(Config, *sql.DB, Handler) (*Bot, error) { return nil, ErrNoE2EE }

func (*Bot) Ready() bool               { return false }
func (*Bot) Run(context.Context) error { return ErrNoE2EE }
func (*Bot) SendText(context.Context, string, string, string) (string, error) {
	return "", ErrNoE2EE
}
func (*Bot) SendVoice(context.Context, string, string, *speech.Audio) (string, error) {
	return "", ErrNoE2EE
}
