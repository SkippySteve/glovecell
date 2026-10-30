package matrix

import (
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	steve = id.UserID("@steve:example.com")
	bot   = id.UserID("@glovecell:example.com")
	room  = id.RoomID("!inbox:example.com")
)

var filter = Filter{Self: bot, Steve: steve, Rooms: map[id.RoomID]bool{room: true}}

func msgEvent(sender id.UserID, c *event.MessageEventContent, raw string) *event.Event {
	evt := &event.Event{Type: event.EventMessage, RoomID: room, ID: "$e1", Sender: sender, Timestamp: time.Now().UnixMilli(),
		Content: event.Content{Parsed: c, VeryRaw: []byte(raw)}}
	evt.Mautrix.WasEncrypted = true
	evt.Mautrix.TrustState = id.TrustStateCrossSignedVerified
	evt.Mautrix.TrustSource = &id.Device{UserID: sender, DeviceID: "PHONE"}
	return evt
}

func TestInspectAcceptsSteveTextAndVoice(t *testing.T) {
	m, why := filter.Inspect(msgEvent(steve, &event.MessageEventContent{MsgType: event.MsgText, Body: " approve 4721 "}, `{}`))
	if why != "" || m.Kind != KindText || m.Body != "approve 4721" || m.DeviceID != "PHONE" || !m.Encrypted || m.ThreadRoot != "$e1" || m.InThread {
		t.Fatalf("text %+v %q", m, why)
	}
	voice := &event.MessageEventContent{MsgType: event.MsgAudio, Body: "Voice message", Info: &event.FileInfo{MimeType: "audio/ogg"}, MSC3245Voice: &event.MSC3245Voice{},
		RelatesTo: (&event.RelatesTo{}).SetThread("$root", "$root")}
	m, why = filter.Inspect(msgEvent(steve, voice, `{}`))
	if why != "" || m.Kind != KindVoice || m.AudioMIME != "audio/ogg" || m.ThreadRoot != "$root" || !m.InThread || m.Body != "" {
		t.Fatalf("voice %+v %q", m, why)
	}
	if m.Trust != "cross-signed-verified" {
		t.Fatalf("trust %q", m.Trust)
	}
}

func TestInspectDropsOthersBotsAndOtherRooms(t *testing.T) {
	text := &event.MessageEventContent{MsgType: event.MsgText, Body: "approve 4721"}
	for name, c := range map[string]struct {
		evt  *event.Event
		want string
	}{
		"other user": {msgEvent("@mallory:example.com", text, `{}`), IgnoreNotSteve},
		"own":        {msgEvent(bot, text, `{}`), IgnoreSelf},
		"other bot":  {msgEvent("@orca-bot:example.com", text, `{}`), IgnoreNotSteve},
		"notice":     {msgEvent(steve, &event.MessageEventContent{MsgType: event.MsgNotice, Body: "approve 4721"}, `{}`), IgnoreMsgType},
		"image":      {msgEvent(steve, &event.MessageEventContent{MsgType: event.MsgImage, Body: "x.png"}, `{}`), IgnoreMsgType},
		"room": {func() *event.Event {
			e := msgEvent(steve, text, `{}`)
			e.RoomID = "!elsewhere:example.com"
			return e
		}(), IgnoreRoom},
		"reaction": {&event.Event{Type: event.EventReaction, RoomID: room, Sender: steve}, IgnoreNotMessage},
	} {
		if _, why := filter.Inspect(c.evt); why != c.want {
			t.Errorf("%s: %q, want %q", name, why, c.want)
		}
	}
	if _, why := (Filter{Self: bot, Rooms: filter.Rooms}).Inspect(msgEvent(steve, text, `{}`)); why != IgnoreNotSteve {
		t.Fatal("empty Steve accepted a sender")
	}
}

func TestInspectFlagsEditsAndForwards(t *testing.T) {
	edit := &event.MessageEventContent{MsgType: event.MsgText, Body: "* approve 4721", NewContent: &event.MessageEventContent{MsgType: event.MsgText, Body: "approve 4721"},
		RelatesTo: &event.RelatesTo{Type: event.RelReplace, EventID: "$orig"}}
	if m, _ := filter.Inspect(msgEvent(steve, edit, `{}`)); !m.Edited {
		t.Fatal("edit not flagged")
	}
	text := &event.MessageEventContent{MsgType: event.MsgText, Body: "approve 4721"}
	if m, _ := filter.Inspect(msgEvent(steve, text, `{"msgtype":"m.text","body":"approve 4721","m.forwarded":{}}`)); !m.Forwarded {
		t.Fatal("forward marker not flagged")
	}
	fk := msgEvent(steve, text, `{}`)
	fk.Mautrix.ForwardedKeys = true
	if m, _ := filter.Inspect(fk); !m.Forwarded {
		t.Fatal("forwarded keys not flagged")
	}
	if m, _ := filter.Inspect(msgEvent(steve, text, `{"msgtype":"m.text"}`)); m.Edited || m.Forwarded {
		t.Fatal("plain message flagged")
	}
}

func TestTrustPolicy(t *testing.T) {
	const pinned = "MSKsteve"
	base := func(state id.TrustState) *event.Event {
		e := msgEvent(steve, &event.MessageEventContent{MsgType: event.MsgText}, `{}`)
		e.Mautrix.TrustState = state
		return e
	}
	cases := []struct {
		name   string
		policy TrustPolicy
		evt    *event.Event
		msk    string
		want   bool
	}{
		{"cross-signed verified", TrustPolicy{}, base(id.TrustStateCrossSignedVerified), "", true},
		{"tofu without pin", TrustPolicy{}, base(id.TrustStateCrossSignedTOFU), "", false},
		{"tofu with pin", TrustPolicy{PinnedMasterKey: pinned}, base(id.TrustStateCrossSignedTOFU), pinned, true},
		{"pin mismatch", TrustPolicy{PinnedMasterKey: pinned}, base(id.TrustStateCrossSignedVerified), "MSKattacker", false},
		{"unknown device", TrustPolicy{PinnedMasterKey: pinned}, base(id.TrustStateUnknownDevice), pinned, false},
		{"not cross-signed", TrustPolicy{}, base(id.TrustStateUnset), "", false},
		{"key backup", TrustPolicy{PinnedMasterKey: pinned}, base(id.TrustStateBackup), pinned, false},
		{"unencrypted", TrustPolicy{}, func() *event.Event {
			e := base(id.TrustStateCrossSignedVerified)
			e.Mautrix.WasEncrypted = false
			return e
		}(), "", false},
		{"forwarded keys", TrustPolicy{}, func() *event.Event {
			e := base(id.TrustStateCrossSignedVerified)
			e.Mautrix.ForwardedKeys = true
			return e
		}(), "", false},
		{"no device", TrustPolicy{}, func() *event.Event {
			e := base(id.TrustStateCrossSignedVerified)
			e.Mautrix.TrustSource = nil
			return e
		}(), "", false},
	}
	for _, c := range cases {
		if got := c.policy.Verified(c.evt, c.msk); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	good := Config{Homeserver: "https://matrix.example.com", UserID: string(bot), Password: "pw", PickleKey: []byte(strings.Repeat("k", 32)), Steve: string(steve), Rooms: []string{string(room)}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"http":       func(c *Config) { c.Homeserver = "http://matrix.example.com" },
		"same user":  func(c *Config) { c.Steve = c.UserID },
		"no creds":   func(c *Config) { c.Password = "" },
		"short key":  func(c *Config) { c.PickleKey = []byte("short") },
		"no rooms":   func(c *Config) { c.Rooms = nil },
		"alias room": func(c *Config) { c.Rooms = []string{"#inbox:example.com"} },
		"bad steve":  func(c *Config) { c.Steve = "steve" },
	} {
		c := good
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		} else if strings.Contains(err.Error(), "pw") {
			t.Errorf("%s: error leaks the password", name)
		}
	}
}

func TestDisabledBuildFailsClosed(t *testing.T) {
	if Enabled {
		t.Skip("built with goolm")
	}
	if _, err := New(Config{}, nil, nil); err == nil {
		t.Fatal("bot without E2EE started")
	}
}
