//go:build goolm

package matrix

// End-to-end test against a real homeserver, with real E2EE:
//
//	GLOVECELL_MATRIX_TEST_HOMESERVER=http://127.0.0.1:8008 \
//	GLOVECELL_MATRIX_TEST_SHARED_SECRET=<registration_shared_secret> \
//	go test -tags goolm -run TestBotE2EE ./internal/voice/matrix
//
// It registers fresh users on a disposable Synapse (shared-secret
// registration), gives "Steve" a cross-signing identity, and checks that the
// bot decrypts his text and voice, marks his cross-signed device verified
// under the pinned master key and his second, unsigned device unverified,
// and that the bot's own voice note decrypts as an Ogg/Opus MSC3245 voice
// message. Skipped without the variables.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store/storetest"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/speech"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

func register(t *testing.T, hs, secret, user, password string) {
	t.Helper()
	resp, err := http.Get(hs + "/_synapse/admin/v1/register")
	if err != nil {
		t.Fatal(err)
	}
	var n struct {
		Nonce string `json:"nonce"`
	}
	json.NewDecoder(resp.Body).Decode(&n)
	resp.Body.Close()
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(n.Nonce + "\x00" + user + "\x00" + password + "\x00notadmin"))
	body, _ := json.Marshal(map[string]any{"nonce": n.Nonce, "username": user, "password": password, "admin": false, "mac": hex.EncodeToString(mac.Sum(nil))})
	resp, err = http.Post(hs+"/_synapse/admin/v1/register", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("register %s: %v %v", user, err, resp.Status)
	}
	resp.Body.Close()
}

type steveClient struct {
	cli    *mautrix.Client
	helper *cryptohelper.CryptoHelper
}

func login(t *testing.T, ctx context.Context, hs, user, password string) *steveClient {
	t.Helper()
	cli, err := mautrix.NewClient(hs, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Login(ctx, &mautrix.ReqLogin{Type: mautrix.AuthTypePassword, Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: user}, Password: password, StoreCredentials: true}); err != nil {
		t.Fatal(err)
	}
	db, err := dbutil.NewWithDB(stdlib.OpenDBFromPool(storetest.Pool(t, storetest.Database(t))), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	helper, err := cryptohelper.NewCryptoHelper(cli, []byte(strings.Repeat("p", 32)), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Init(ctx); err != nil {
		t.Fatal(err)
	}
	cli.Crypto = helper
	go cli.SyncWithContext(ctx)
	return &steveClient{cli: cli, helper: helper}
}

func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestBotE2EE(t *testing.T) {
	hs, secret := os.Getenv("GLOVECELL_MATRIX_TEST_HOMESERVER"), os.Getenv("GLOVECELL_MATRIX_TEST_SHARED_SECRET")
	if hs == "" || secret == "" {
		t.Skip("set GLOVECELL_MATRIX_TEST_HOMESERVER and GLOVECELL_MATRIX_TEST_SHARED_SECRET to run against a disposable Synapse")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var b [4]byte
	rand.Read(b[:])
	suffix := hex.EncodeToString(b[:])
	steveName, botName, pw := "steve"+suffix, "glovecell"+suffix, "pw-"+suffix+"-0123456789"
	register(t, hs, secret, steveName, pw)
	register(t, hs, secret, botName, pw)
	host := strings.TrimPrefix(strings.TrimPrefix(hs, "http://"), "https://")
	server := "localhost"
	if !strings.HasPrefix(host, "127.0.0.1") && !strings.HasPrefix(host, "localhost") {
		server = host
	}
	steveID := id.NewUserID(steveName, server)
	botID := id.NewUserID(botName, server)

	// Steve: cross-signing identity, phone device signed.
	phone := login(t, ctx, hs, steveName, pw)
	mach := phone.helper.Machine()
	if _, _, err := mach.GenerateAndUploadCrossSigningKeysWithPassword(ctx, pw, ""); err != nil {
		t.Fatal(err)
	}
	if err := mach.SignOwnDevice(ctx, mach.OwnIdentity()); err != nil {
		t.Fatal(err)
	}
	if err := mach.SignOwnMasterKey(ctx); err != nil {
		t.Fatal(err)
	}
	msk := mach.CrossSigningKeys.MasterKey.PublicKey().String()

	room, err := phone.cli.CreateRoom(ctx, &mautrix.ReqCreateRoom{Preset: "private_chat", Invite: []id.UserID{botID},
		InitialState: []*event.Event{{Type: event.StateEncryption, Content: event.Content{Parsed: &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}}}}})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []Message
	bot, err := New(Config{Homeserver: hs, UserID: botID.String(), Password: pw, DeviceName: "glovecell-voice-test", PickleKey: []byte(strings.Repeat("k", 32)),
		Steve: steveID.String(), Rooms: []string{room.RoomID.String()}, Trust: TrustPolicy{PinnedMasterKey: msk}, MaxEventAge: time.Hour},
		stdlib.OpenDBFromPool(storetest.Pool(t, storetest.Database(t))),
		func(_ context.Context, m Message) { mu.Lock(); got = append(got, m); mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := bot.Run(ctx); err != nil {
			t.Errorf("bot: %v", err)
		}
	}()
	eventually(t, "the bot to join", func() bool {
		members, err := phone.cli.JoinedMembers(ctx, room.RoomID)
		return err == nil && members.Joined[botID].DisplayName != "" || err == nil && len(members.Joined) == 2
	})
	eventually(t, "Steve's client to see the bot and encryption", func() bool {
		enc, _ := phone.cli.StateStore.IsEncrypted(ctx, room.RoomID)
		m, _ := phone.cli.StateStore.GetMember(ctx, room.RoomID, botID)
		return enc && m != nil && m.Membership == event.MembershipJoin && bot.Ready()
	})
	received := func(n int) []Message {
		eventually(t, fmt.Sprintf("%d messages at the bot", n), func() bool { mu.Lock(); defer mu.Unlock(); return len(got) >= n })
		mu.Lock()
		defer mu.Unlock()
		return append([]Message(nil), got...)
	}

	// 1. Text from the cross-signed phone: verified under the pinned key.
	if _, err := phone.cli.SendMessageEvent(ctx, room.RoomID, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgText, Body: "approve 4721"}); err != nil {
		t.Fatal(err)
	}
	m := received(1)[0]
	if m.Body != "approve 4721" || !m.Encrypted || !m.DeviceVerified || m.DeviceID != phone.cli.DeviceID.String() || m.Sender != steveID.String() {
		t.Fatalf("text message %+v", m)
	}

	// 2. An encrypted voice note: the bot downloads and decrypts the audio.
	voice := speech.SyntheticVoice(2 * time.Second)
	file := attachment.NewEncryptedFile()
	enc := append([]byte(nil), voice...)
	file.EncryptInPlace(enc)
	up, err := phone.cli.UploadBytes(ctx, enc, "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phone.cli.SendMessageEvent(ctx, room.RoomID, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgAudio, Body: "Voice message",
		Info: &event.FileInfo{MimeType: "audio/ogg"}, MSC3245Voice: &event.MSC3245Voice{}, File: &event.EncryptedFileInfo{EncryptedFile: *file, URL: up.ContentURI.CUString()}}); err != nil {
		t.Fatal(err)
	}
	m = received(2)[1]
	if m.Kind != KindVoice || !bytes.Equal(m.Audio, voice) || m.AudioSHA256 != AudioHash(voice) || !m.DeviceVerified {
		t.Fatalf("voice message kind %s audio %d bytes verified %v", m.Kind, len(m.Audio), m.DeviceVerified)
	}

	// 3. A second, unsigned device of Steve's: delivered, never verified.
	laptop := login(t, ctx, hs, steveName, pw)
	eventually(t, "the second device to see the room", func() bool {
		enc, _ := laptop.cli.StateStore.IsEncrypted(ctx, room.RoomID)
		return enc
	})
	if _, err := laptop.cli.SendMessageEvent(ctx, room.RoomID, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgText, Body: "approve 9999"}); err != nil {
		t.Fatal(err)
	}
	m = received(3)[2]
	if m.Body != "approve 9999" || m.DeviceVerified || m.DeviceID != laptop.cli.DeviceID.String() {
		t.Fatalf("unsigned device message %+v", m)
	}

	// 4. The bot's voice reply decrypts as an Ogg/Opus MSC3245 voice note.
	var reply *event.Event
	var replyMu sync.Mutex
	phone.cli.Syncer.(*mautrix.DefaultSyncer).OnEventType(event.EventMessage, func(_ context.Context, evt *event.Event) {
		if evt.Sender == botID {
			replyMu.Lock()
			reply = evt
			replyMu.Unlock()
		}
	})
	audio, err := speech.Analyze(speech.SyntheticVoice(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bot.SendVoice(ctx, room.RoomID.String(), m.ThreadRoot, audio); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the bot's voice note at Steve's phone", func() bool { replyMu.Lock(); defer replyMu.Unlock(); return reply != nil })
	c := reply.Content.AsMessage()
	if c.MsgType != event.MsgAudio || c.MSC3245Voice == nil || c.MSC1767Audio == nil || len(c.MSC1767Audio.Waveform) != speech.WaveformBins || c.File == nil || !reply.Mautrix.WasEncrypted {
		t.Fatalf("voice reply content %s", reply.Content.VeryRaw)
	}
	if c.RelatesTo == nil || c.RelatesTo.Type != event.RelThread || c.RelatesTo.EventID.String() != m.ThreadRoot {
		t.Fatalf("voice reply not in the thread: %+v", c.RelatesTo)
	}
	mxc, _ := c.File.URL.Parse()
	data, err := phone.cli.DownloadBytes(ctx, mxc)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.File.DecryptInPlace(data); err != nil {
		t.Fatal(err)
	}
	if _, err := speech.ParseOggOpus(data); err != nil {
		t.Fatalf("voice reply is not Ogg/Opus: %v", err)
	}
}
