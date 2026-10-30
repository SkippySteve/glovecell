package matrix

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ErrNoE2EE is returned by New in builds without the goolm tag.
var ErrNoE2EE = errors.New("this build has no Matrix end-to-end encryption; build with -tags goolm")

// Config is the bot account and who it listens to. Credentials come from
// mounted Secret files (cmd/glovecell-voice) and never appear in errors.
type Config struct {
	// Homeserver is the client-server API origin, for example
	// https://matrix.example.com.
	Homeserver string
	// UserID is the bot's MXID, for example @glovecell:example.com.
	UserID string
	// Password signs the bot in on first start; the crypto store then
	// keeps the device ID so later starts reuse the same device. Leave
	// empty when AccessToken and DeviceID are set instead.
	Password    string
	AccessToken string
	DeviceID    string
	DeviceName  string
	// PickleKey encrypts the Olm account and sessions at rest in
	// PostgreSQL. At least 32 bytes.
	PickleKey []byte
	// Steve is the only MXID whose messages are handled.
	Steve string
	// Rooms are the room IDs the bot works in.
	Rooms []string
	Trust TrustPolicy
	// MaxEventAge drops older events (for example history on first sync).
	MaxEventAge time.Duration
}

// Validate checks the configuration without contacting the homeserver.
func (c Config) Validate() error {
	u, err := url.Parse(c.Homeserver)
	if err != nil || u.Scheme != "https" && !(u.Scheme == "http" && isLocal(u.Hostname())) || u.Host == "" || u.User != nil {
		return errors.New("matrix homeserver must be an https URL (http only for localhost)")
	}
	if !validMXID(c.UserID) || !validMXID(c.Steve) {
		return errors.New("matrix bot user and Steve's user must be full MXIDs (@name:server)")
	}
	if c.UserID == c.Steve {
		return errors.New("the bot must be a separate Matrix user from Steve")
	}
	if c.Password == "" && (c.AccessToken == "" || c.DeviceID == "") {
		return errors.New("matrix bot needs a password, or an access token with its device ID")
	}
	if len(c.PickleKey) < 32 {
		return errors.New("matrix pickle key must be at least 32 bytes")
	}
	if len(c.Rooms) == 0 {
		return errors.New("matrix bot needs at least one room")
	}
	for _, r := range c.Rooms {
		if !strings.HasPrefix(r, "!") || !strings.Contains(r, ":") {
			return fmt.Errorf("matrix room %q must be a room ID (!id:server)", r)
		}
	}
	return nil
}

func validMXID(s string) bool {
	return strings.HasPrefix(s, "@") && strings.Count(s, ":") >= 1 && !strings.ContainsAny(s, " \t\n") && len(s) <= 255
}

func isLocal(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }
