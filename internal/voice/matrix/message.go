// Package matrix is glovecell-voice's Matrix side: a bot account with
// end-to-end encryption (mautrix-go and its crypto helper, crypto state in
// PostgreSQL) that receives Steve's voice and text messages in configured
// rooms and replies in threads with text and voice notes.
//
// Only messages from the configured Steve account reach the service; the
// bot's own messages, other users and bots are dropped here. Each Message
// carries what the approval gate needs to decide whether it counts: the
// sending device, whether that device is cross-signed and verified, whether
// the event was encrypted, and whether it is an edit or a forward.
//
// The E2EE client (bot.go) needs the goolm build tag, which selects
// mautrix's pure-Go Olm. Without it, New fails closed (bot_disabled.go), so
// the rest of the module builds and tests with or without cgo.
package matrix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/speech"
)

// Kind is what Steve sent.
type Kind string

const (
	KindText  Kind = "text"
	KindVoice Kind = "voice"
)

// Message is one message from Steve, decrypted, with its provenance.
type Message struct {
	RoomID  string
	EventID string
	// ThreadRoot is the thread to reply in: the message's own thread, or
	// the message itself when it starts one.
	ThreadRoot string
	// InThread reports that the message was sent inside an existing thread.
	InThread bool
	Sender   string
	DeviceID string
	Kind     Kind
	// Body is the text, or the caption of a voice message.
	Body string
	// Audio is the downloaded, decrypted voice note, and AudioSHA256 its hash.
	Audio       []byte
	AudioMIME   string
	AudioSHA256 string
	// Encrypted reports that the event arrived end-to-end encrypted.
	Encrypted bool
	// DeviceVerified reports that the sending device is cross-signed by
	// Steve's self-signing key and Steve's identity is verified (or pinned).
	DeviceVerified bool
	// Trust is the trust state for the audit, for example
	// "cross-signed-verified" or "unknown-device".
	Trust     string
	Edited    bool
	Forwarded bool
	Time      time.Time
}

// Handler receives Steve's messages.
type Handler func(context.Context, Message)

// Sender posts into rooms. Every reply goes into a thread.
type Sender interface {
	SendText(ctx context.Context, roomID, threadRoot, text string) (string, error)
	SendVoice(ctx context.Context, roomID, threadRoot string, audio *speech.Audio) (string, error)
}

// Filter decides which events become Messages.
type Filter struct {
	Self  id.UserID
	Steve id.UserID
	Rooms map[id.RoomID]bool
}

// Ignore reasons.
const (
	IgnoreNotMessage = "not a message"
	IgnoreRoom       = "room not configured"
	IgnoreSelf       = "own message"
	IgnoreNotSteve   = "sender is not the configured user"
	IgnoreMsgType    = "unsupported message type"
)

// forwardKeys are content keys clients use to mark a forwarded message.
var forwardKeys = []string{"m.forwarded", "org.matrix.msc2723.forward_info", "io.element.forwarded"}

// Inspect turns a decrypted event into a Message, or returns why it is
// ignored. Audio is not downloaded here; the caller fills it in.
func (f Filter) Inspect(evt *event.Event) (Message, string) {
	if evt == nil || evt.Type != event.EventMessage {
		return Message{}, IgnoreNotMessage
	}
	if !f.Rooms[evt.RoomID] {
		return Message{}, IgnoreRoom
	}
	if evt.Sender == f.Self {
		return Message{}, IgnoreSelf
	}
	if f.Steve == "" || evt.Sender != f.Steve {
		return Message{}, IgnoreNotSteve
	}
	content := evt.Content.AsMessage()
	if content == nil {
		return Message{}, IgnoreNotMessage
	}
	m := Message{
		RoomID:    evt.RoomID.String(),
		EventID:   evt.ID.String(),
		Sender:    evt.Sender.String(),
		Encrypted: evt.Mautrix.WasEncrypted,
		Time:      time.UnixMilli(evt.Timestamp),
		Body:      strings.TrimSpace(content.Body),
	}
	if evt.Mautrix.TrustSource != nil {
		m.DeviceID = evt.Mautrix.TrustSource.DeviceID.String()
	}
	m.Trust = trustLabel(evt.Mautrix.TrustState)
	rel := content.RelatesTo
	m.Edited = content.NewContent != nil || (rel != nil && rel.Type == event.RelReplace)
	m.Forwarded = evt.Mautrix.ForwardedKeys || evt.Mautrix.TrustState == id.TrustStateForwarded || hasForwardMarker(evt)
	m.ThreadRoot = m.EventID
	if rel != nil && rel.Type == event.RelThread && rel.EventID != "" {
		m.ThreadRoot = rel.EventID.String()
		m.InThread = true
	}
	switch content.MsgType {
	case event.MsgText:
		m.Kind = KindText
	case event.MsgAudio:
		m.Kind = KindVoice
		if content.FileName == "" || content.FileName == content.Body {
			m.Body = ""
		}
		if content.Info != nil {
			m.AudioMIME = content.Info.MimeType
		}
	default:
		// m.notice is how bots talk; images, files and emotes are not tasks.
		return Message{}, IgnoreMsgType
	}
	return m, ""
}

func hasForwardMarker(evt *event.Event) bool {
	raw := evt.Content.VeryRaw
	if len(raw) == 0 {
		raw, _ = json.Marshal(evt.Content.Raw)
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return false
	}
	for _, k := range forwardKeys {
		if _, ok := top[k]; ok {
			return true
		}
	}
	return false
}

func trustLabel(t id.TrustState) string {
	switch t {
	case id.TrustStateVerified:
		return "verified"
	case id.TrustStateCrossSignedVerified:
		return "cross-signed-verified"
	case id.TrustStateCrossSignedTOFU:
		return "cross-signed-tofu"
	case id.TrustStateCrossSignedUntrusted:
		return "cross-signed-untrusted"
	case id.TrustStateUnknownDevice:
		return "unknown-device"
	case id.TrustStateForwarded:
		return "forwarded"
	case id.TrustStateBackup:
		return "key-backup"
	case id.TrustStateDeviceKeyMismatch:
		return "device-key-mismatch"
	case id.TrustStateBlacklisted:
		return "blacklisted"
	}
	return "unset"
}

// TrustPolicy decides whether a decrypted event's sending device counts as
// cross-signed and verified. Trust is pinned: the device must be
// cross-signed by Steve's self-signing key under exactly the pinned master
// key. "Verified by someone" is not enough, and a reset or replaced identity
// (a new master key) is never trusted until the pin is changed.
type TrustPolicy struct {
	// PinnedMasterKey is Steve's cross-signing master key, unpadded base64
	// as /keys/query returns it. Required: without it nothing is verified.
	PinnedMasterKey string
}

// NormalizeKey strips the whitespace clients use to group key characters.
func NormalizeKey(k string) string { return strings.Join(strings.Fields(k), "") }

// Verified applies the policy. currentMasterKey is the master key the bot's
// crypto store holds for the sender now ("" if unknown).
func (p TrustPolicy) Verified(evt *event.Event, currentMasterKey string) bool {
	pin := NormalizeKey(p.PinnedMasterKey)
	if pin == "" || currentMasterKey == "" || currentMasterKey != pin {
		return false
	}
	if !evt.Mautrix.WasEncrypted || evt.Mautrix.ForwardedKeys || evt.Mautrix.TrustSource == nil {
		return false
	}
	switch evt.Mautrix.TrustState {
	case id.TrustStateCrossSignedVerified, id.TrustStateCrossSignedTOFU, id.TrustStateCrossSignedUntrusted:
		// Cross-signed by the self-signing key under the current master
		// key, which equals the pin (checked above).
		return true
	}
	// Unset (not cross-signed), unknown device, key backup, forwarded keys,
	// key mismatch, blacklisted, and TrustStateVerified (a local
	// verification, not cross-signing) all fail.
	return false
}

// AudioHash returns the hex SHA-256 of audio.
func AudioHash(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
