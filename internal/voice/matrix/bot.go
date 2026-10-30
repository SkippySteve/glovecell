//go:build goolm

package matrix

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/speech"
)

// Enabled reports whether this build has the E2EE Matrix client.
const Enabled = true

// Bot is the E2EE Matrix client.
type Bot struct {
	cfg     Config
	cli     *mautrix.Client
	crypto  *cryptohelper.CryptoHelper
	filter  Filter
	handler Handler
	ready   atomic.Bool
	started time.Time
	wg      sync.WaitGroup
}

var _ Sender = (*Bot)(nil)

// New logs nothing in and contacts nobody; Run does.
func New(cfg Config, db *sql.DB, handler Handler) (*Bot, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if db == nil || handler == nil {
		return nil, errors.New("matrix bot needs a database and a handler")
	}
	cli, err := mautrix.NewClient(cfg.Homeserver, id.UserID(cfg.UserID), cfg.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("matrix client: %w", err)
	}
	cli.Log = zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) { w.NoColor = true })).Level(zerolog.WarnLevel).With().Str("component", "matrix").Logger()
	if cfg.DeviceID != "" {
		cli.DeviceID = id.DeviceID(cfg.DeviceID)
	}
	mdb, err := dbutil.NewWithDB(db, "postgres")
	if err != nil {
		return nil, err
	}
	helper, err := cryptohelper.NewCryptoHelper(cli, cfg.PickleKey, mdb)
	if err != nil {
		return nil, err
	}
	if cfg.AccessToken == "" {
		helper.LoginAs = &mautrix.ReqLogin{
			Type:                     mautrix.AuthTypePassword,
			Identifier:               mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: cfg.UserID},
			Password:                 cfg.Password,
			InitialDeviceDisplayName: cfg.DeviceName,
		}
	}
	rooms := map[id.RoomID]bool{}
	for _, r := range cfg.Rooms {
		rooms[id.RoomID(r)] = true
	}
	b := &Bot{cfg: cfg, cli: cli, crypto: helper, handler: handler, filter: Filter{Self: id.UserID(cfg.UserID), Steve: id.UserID(cfg.Steve), Rooms: rooms}}
	return b, nil
}

// Ready reports whether the bot has synced since start.
func (b *Bot) Ready() bool { return b.ready.Load() }

// Run initialises encryption, joins the configured rooms and syncs until ctx
// ends, retrying with backoff.
func (b *Bot) Run(ctx context.Context) error {
	if err := b.crypto.Init(ctx); err != nil {
		return fmt.Errorf("matrix encryption setup: %w", err)
	}
	b.cli.Crypto = b.crypto
	b.started = time.Now()
	slog.Info("matrix bot signed in", "user", b.cfg.UserID, "device", b.cli.DeviceID.String())
	// Show Steve's current master key so the operator can compare it with
	// his own client before pinning it. It is public key material.
	if msk := b.masterKey(ctx, id.UserID(b.cfg.Steve)); msk != "" {
		slog.Info("Steve's cross-signing master key as the bot sees it", "masterKey", msk, "pinned", b.cfg.Trust.PinnedMasterKey != "", "matchesPin", msk == b.cfg.Trust.PinnedMasterKey)
	} else {
		slog.Warn("Steve has no cross-signing master key yet; no device of his counts as verified")
	}
	syncer := b.cli.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnSync(func(context.Context, *mautrix.RespSync, string) bool {
		b.ready.Store(true)
		return true
	})
	syncer.OnEventType(event.EventMessage, b.onMessage)
	syncer.OnEventType(event.StateMember, b.onMember)
	for _, r := range b.cfg.Rooms {
		if _, err := b.cli.JoinRoom(ctx, r, nil); err != nil {
			slog.Warn("matrix room not joined yet; invite the bot", "room", r, "error", err.Error())
		}
	}
	backoff := time.Second
	for {
		err := b.cli.SyncWithContext(ctx)
		if ctx.Err() != nil {
			b.wg.Wait()
			_ = b.crypto.Close()
			return nil
		}
		b.ready.Store(false)
		slog.Warn("matrix sync stopped; retrying", "error", errString(err), "in", backoff.String())
		select {
		case <-ctx.Done():
			continue
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (b *Bot) onMember(ctx context.Context, evt *event.Event) {
	if evt.GetStateKey() != b.cfg.UserID || !b.filter.Rooms[evt.RoomID] {
		return
	}
	if m := evt.Content.AsMember(); m != nil && m.Membership == event.MembershipInvite && evt.Sender == b.filter.Steve {
		if _, err := b.cli.JoinRoom(ctx, evt.RoomID.String(), nil); err != nil {
			slog.Warn("matrix join failed", "room", evt.RoomID.String(), "error", err.Error())
		}
	}
}

func (b *Bot) onMessage(ctx context.Context, evt *event.Event) {
	msg, reason := b.filter.Inspect(evt)
	if reason != "" {
		if reason != IgnoreSelf && reason != IgnoreRoom {
			slog.Debug("matrix event ignored", "event", evt.ID.String(), "reason", reason)
		}
		return
	}
	if age := b.cfg.MaxEventAge; age > 0 && time.Since(msg.Time) > age {
		slog.Info("matrix event too old; ignored", "event", msg.EventID, "age", time.Since(msg.Time).Round(time.Second).String())
		return
	}
	msg.DeviceVerified = b.cfg.Trust.Verified(evt, b.masterKey(ctx, evt.Sender))
	if msg.Kind == KindVoice {
		audio, err := b.download(ctx, evt.Content.AsMessage())
		if err != nil {
			slog.Warn("matrix voice download failed", "event", msg.EventID, "error", err.Error())
			_, _ = b.SendText(ctx, msg.RoomID, msg.ThreadRoot, "I couldn't download that voice message.")
			return
		}
		msg.Audio, msg.AudioSHA256 = audio, AudioHash(audio)
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.handler(context.WithoutCancel(ctx), msg)
	}()
}

// masterKey returns the sender's current cross-signing master key.
func (b *Bot) masterKey(ctx context.Context, user id.UserID) string {
	mach := b.crypto.Machine()
	if mach == nil {
		return ""
	}
	keys, err := mach.GetCrossSigningPublicKeys(ctx, user)
	if err != nil || keys == nil {
		return ""
	}
	return keys.MasterKey.String()
}

func (b *Bot) download(ctx context.Context, content *event.MessageEventContent) ([]byte, error) {
	var uri id.ContentURIString
	if content.File != nil {
		uri = content.File.URL
	} else {
		uri = content.URL
	}
	mxc, err := uri.Parse()
	if err != nil {
		return nil, fmt.Errorf("bad media URL: %w", err)
	}
	resp, err := b.cli.Download(ctx, mxc)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, speech.MaxAudioBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > speech.MaxAudioBytes {
		return nil, errors.New("voice message too large")
	}
	if content.File != nil {
		if err := content.File.DecryptInPlace(data); err != nil {
			return nil, fmt.Errorf("decrypt media: %w", err)
		}
	}
	return data, nil
}

func threadRelation(root string) *event.RelatesTo {
	if root == "" {
		return nil
	}
	return (&event.RelatesTo{}).SetThread(id.EventID(root), id.EventID(root))
}

// SendText posts a text message in the thread.
func (b *Bot) SendText(ctx context.Context, roomID, threadRoot, text string) (string, error) {
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: text, RelatesTo: threadRelation(threadRoot)}
	resp, err := b.cli.SendMessageEvent(ctx, id.RoomID(roomID), event.EventMessage, content)
	if err != nil {
		return "", err
	}
	return resp.EventID.String(), nil
}

// SendVoice uploads an Ogg/Opus voice note (encrypted in encrypted rooms)
// and posts it as an MSC3245 voice message, which Element X shows with a
// waveform and plays inline.
// The text goes in a separate message, so the voice note has no caption.
func (b *Bot) SendVoice(ctx context.Context, roomID, threadRoot string, audio *speech.Audio) (string, error) {
	if audio == nil || len(audio.Data) == 0 {
		return "", errors.New("no audio")
	}
	room := id.RoomID(roomID)
	encrypted, err := b.cli.StateStore.IsEncrypted(ctx, room)
	if err != nil {
		return "", err
	}
	data := append([]byte(nil), audio.Data...)
	ms := int(audio.Duration / time.Millisecond)
	content := &event.MessageEventContent{
		MsgType:      event.MsgAudio,
		Body:         "Voice message",
		Info:         &event.FileInfo{MimeType: "audio/ogg", Size: len(data), Duration: ms},
		MSC1767Audio: &event.MSC1767Audio{Duration: ms, Waveform: audio.Waveform},
		MSC3245Voice: &event.MSC3245Voice{},
		RelatesTo:    threadRelation(threadRoot),
	}
	uploadType := "audio/ogg"
	var file *attachment.EncryptedFile
	if encrypted {
		file = attachment.NewEncryptedFile()
		file.EncryptInPlace(data)
		uploadType = "application/octet-stream"
	}
	up, err := b.cli.UploadBytes(ctx, data, uploadType)
	if err != nil {
		return "", fmt.Errorf("upload voice: %w", err)
	}
	if file != nil {
		content.File = &event.EncryptedFileInfo{EncryptedFile: *file, URL: up.ContentURI.CUString()}
	} else {
		content.URL = up.ContentURI.CUString()
	}
	// Marshal with encoding/json and send the raw JSON: mautrix's canonical
	// JSON encoder (json/v2 under Go 1.27) drops empty objects marked
	// omitempty, which would lose "org.matrix.msc3245.voice": {} and with it
	// the voice-message rendering. The integration test checks this.
	raw, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	resp, err := b.cli.SendMessageEvent(ctx, room, event.EventMessage, json.RawMessage(raw))
	if err != nil {
		return "", err
	}
	return resp.EventID.String(), nil
}
