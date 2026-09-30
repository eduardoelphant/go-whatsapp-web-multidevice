# G4 Stable Payload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every message webhook event carries a closed, versioned `payload.stable` object; the CRM can fetch media through `GET /message/:message_id/media`; a JSON Schema plus synthetic and real fixtures pin the contract; a production audit keeps the fixtures honest.

**Architecture:** A pure package `src/pkg/stablepayload` turns whatsmeow events into Go structs whose JSON always carries every key. Two one-line hooks attach it to the existing payloads. A separate Go module `contract/` owns the schema, fixtures, validation test and the `compare` command, so `src/go.mod` does not change. Media streaming is a new usecase + handler; the audit is a new file enabled by an env var.

**Tech Stack:** Go 1.26 (`GOTOOLCHAIN=auto`), whatsmeow (`waE2E` protos), Fiber v3, `github.com/santhosh-tekuri/jsonschema/v6` (contract module only).

**Spec:** `docs/specs/2026-09-25-gateway-g4-stable-payload-design.md`

## Global Constraints

- Branch `elphant` of the fork; commits in English, `type(scope): subject`, no attribution line.
- Pushes, tags, stack updates and anything on noria/GitHub need the owner's OK at that moment (Tasks 11-12 are **[GATED]**).
- Fork logic in new files; upstream files get one-line hooks marked `// Fork (elphant):`.
- `src/go.mod` must not change. The JSON Schema library lives only in `contract/go.mod`.
- `stable` rules: every key always present; unknown value is `null`; a key never changes type; `schema` is `1`.
- Event names exactly: `message`, `message.edited`, `message.reaction`, `message.revoked`, `message.ack`.
- `message` types exactly: `text`, `image`, `video`, `audio`, `document`, `sticker`, `location`, `contact`, `poll`, `unknown`. `media.kind`: `image`, `video`, `audio`, `document`, `sticker`. Ack `status`: `delivered`, `read`, `played`.
- `media.url` is exactly `/message/<id>/media`.
- Fixture file format: `{"event": "<event>", "stable": {…}}`, indented with 2 spaces, trailing newline.
- Audit env var: `WHATSAPP_STABLE_AUDIT_DIR`; at most 3 samples per shape; never blocks or fails delivery.
- Real samples are committed only after the owner reviews them (the fork is public).
- Go commands run from `src/` (or `contract/`) with `GOTOOLCHAIN=auto`.

## Review Focus

1. **A message whose content sits in an unexpected wrapper** (device-sent + ephemeral + view-once together): `type` and `text` must still come out right. Test in Task 2 (`device-sent ephemeral view-once image`).
2. **A JSON `null` slice**: `contact.phones` and ack `ids` must serialize as `[]`, never `null`. Tests in Tasks 2 and 3.
3. **A media download that fails mid-way or a client that disconnects**: the temp file must be removed and the error mapped (404/410/500). Test in Task 9 (`removes the temp dir on download error`).
4. **A slow or failing audit disk**: delivery must not wait. Test in Task 8 (`auditStable returns immediately`).
5. **A reaction removal** (`text == ""`): `emoji` must be `null`, not `""`. Test in Task 3.

---

### Task 1: `stablepayload` types, base and JID resolution

**Files:**
- Create: `src/pkg/stablepayload/types.go`
- Create: `src/pkg/stablepayload/base.go`
- Test: `src/pkg/stablepayload/base_test.go`

**Interfaces:**
- Produces:
  - constants `SchemaVersion = 1`; `EventMessage`, `EventEdited`, `EventReaction`, `EventRevoked`, `EventAck`; `TypeText`, `TypeImage`, `TypeVideo`, `TypeAudio`, `TypeDocument`, `TypeSticker`, `TypeLocation`, `TypeContact`, `TypePoll`, `TypeUnknown`
  - `type Resolver interface { PNForLID(ctx context.Context, lid types.JID) (types.JID, bool); LIDForPN(ctx context.Context, pn types.JID) (types.JID, bool) }`
  - structs `Chat`, `Sender`, `Party`, `Base`, `Media`, `Location`, `Contact`, `Quoted`, `Message`, `Edited`, `Reaction`, `Revoked`, `Ack` (fields below)
  - `func base(ctx context.Context, info types.MessageInfo, r Resolver) Base`
  - `func resolve(ctx context.Context, jid, alt types.JID, r Resolver) (pn, lid *string)`
  - `func strPtr(s string) *string` (nil for "")

- [ ] **Step 1: Write the failing test**

Create `src/pkg/stablepayload/base_test.go`:

```go
package stablepayload

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

type fakeResolver struct {
	pnForLID map[string]string
	lidForPN map[string]string
}

func (f fakeResolver) PNForLID(_ context.Context, lid types.JID) (types.JID, bool) {
	if v, ok := f.pnForLID[lid.String()]; ok {
		j, _ := types.ParseJID(v)
		return j, true
	}
	return types.JID{}, false
}

func (f fakeResolver) LIDForPN(_ context.Context, pn types.JID) (types.JID, bool) {
	if v, ok := f.lidForPN[pn.String()]; ok {
		j, _ := types.ParseJID(v)
		return j, true
	}
	return types.JID{}, false
}

var (
	testTime   = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	pnContact  = types.NewJID("5511900000001", types.DefaultUserServer)
	lidContact = types.NewJID("100000000000001", types.HiddenUserServer)
	pnMe       = types.NewJID("5511900000009", types.DefaultUserServer)
	groupJID   = types.NewJID("120363000000000001", types.GroupServer)
	resolver   = fakeResolver{
		pnForLID: map[string]string{lidContact.String(): pnContact.String()},
		lidForPN: map[string]string{pnContact.String(): lidContact.String()},
	}
)

func toJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestBaseDirectMessageFromPN(t *testing.T) {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{Chat: pnContact, Sender: pnContact},
		ID:            "MSG1", PushName: "Contact One", Timestamp: testTime,
	}
	got := toJSON(t, base(context.Background(), info, resolver))
	want := `{"schema":1,"id":"MSG1","timestamp":"2026-09-25T12:00:00Z","is_from_me":false,` +
		`"chat":{"pn":"5511900000001@s.whatsapp.net","lid":"100000000000001@lid","is_group":false},` +
		`"sender":{"pn":"5511900000001@s.whatsapp.net","lid":"100000000000001@lid","push_name":"Contact One"}}`
	if got != want {
		t.Fatalf("base =\n%s\nwant\n%s", got, want)
	}
}

func TestBaseLIDSenderWithoutMapping(t *testing.T) {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{Chat: lidContact, Sender: lidContact},
		ID:            "MSG2", Timestamp: testTime,
	}
	got := toJSON(t, base(context.Background(), info, fakeResolver{}))
	want := `{"schema":1,"id":"MSG2","timestamp":"2026-09-25T12:00:00Z","is_from_me":false,` +
		`"chat":{"pn":null,"lid":"100000000000001@lid","is_group":false},` +
		`"sender":{"pn":null,"lid":"100000000000001@lid","push_name":null}}`
	if got != want {
		t.Fatalf("base =\n%s\nwant\n%s", got, want)
	}
}

func TestBaseUsesAltJIDBeforeResolver(t *testing.T) {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{Chat: lidContact, Sender: lidContact, SenderAlt: pnContact},
		ID:            "MSG3", Timestamp: testTime,
	}
	got := base(context.Background(), info, nil)
	if got.Sender.PN == nil || *got.Sender.PN != pnContact.String() {
		t.Fatalf("sender.pn = %v, want %s from SenderAlt", got.Sender.PN, pnContact)
	}
	if got.Chat.PN == nil || *got.Chat.PN != pnContact.String() {
		t.Fatalf("chat.pn = %v, want %s from SenderAlt", got.Chat.PN, pnContact)
	}
}

func TestBaseGroupAndOwnMessage(t *testing.T) {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{Chat: groupJID, Sender: pnMe, IsFromMe: true, IsGroup: true},
		ID:            "MSG4", Timestamp: testTime,
	}
	got := toJSON(t, base(context.Background(), info, nil))
	want := `{"schema":1,"id":"MSG4","timestamp":"2026-09-25T12:00:00Z","is_from_me":true,` +
		`"chat":{"pn":"120363000000000001@g.us","lid":null,"is_group":true},` +
		`"sender":{"pn":"5511900000009@s.whatsapp.net","lid":null,"push_name":null}}`
	if got != want {
		t.Fatalf("base =\n%s\nwant\n%s", got, want)
	}
}

func TestResolveStripsDeviceSuffix(t *testing.T) {
	withDevice := types.NewADJID("5511900000001", 0, 12)
	pn, lid := resolve(context.Background(), withDevice, types.JID{}, nil)
	if pn == nil || *pn != "5511900000001@s.whatsapp.net" || lid != nil {
		t.Fatalf("resolve = (%v, %v), want (5511900000001@s.whatsapp.net, nil)", pn, lid)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/stablepayload/ -count=1`
Expected: FAIL to compile (`undefined: base`, `undefined: resolve`).

- [ ] **Step 3: Write `types.go`**

```go
// Package stablepayload builds payload.stable, the fork's closed and versioned
// view of message webhook events. Every key is always present, unknown values
// are null, and a key never changes type. Contract: contract/stable.schema.json.
package stablepayload

import (
	"context"

	"go.mau.fi/whatsmeow/types"
)

// SchemaVersion is stable.schema. Bump it when a field is added.
const SchemaVersion = 1

// Event names, equal to the webhook "event" values.
const (
	EventMessage  = "message"
	EventEdited   = "message.edited"
	EventReaction = "message.reaction"
	EventRevoked  = "message.revoked"
	EventAck      = "message.ack"
)

// Message types (stable.type).
const (
	TypeText     = "text"
	TypeImage    = "image"
	TypeVideo    = "video"
	TypeAudio    = "audio"
	TypeDocument = "document"
	TypeSticker  = "sticker"
	TypeLocation = "location"
	TypeContact  = "contact"
	TypePoll     = "poll"
	TypeUnknown  = "unknown"
)

// Resolver maps between phone-number JIDs and LID JIDs. A failed or empty
// lookup returns ok=false and the builder emits null.
type Resolver interface {
	PNForLID(ctx context.Context, lid types.JID) (pn types.JID, ok bool)
	LIDForPN(ctx context.Context, pn types.JID) (lid types.JID, ok bool)
}

type Chat struct {
	PN      *string `json:"pn"`
	LID     *string `json:"lid"`
	IsGroup bool    `json:"is_group"`
}

type Sender struct {
	PN       *string `json:"pn"`
	LID      *string `json:"lid"`
	PushName *string `json:"push_name"`
}

type Party struct {
	PN  *string `json:"pn"`
	LID *string `json:"lid"`
}

type Base struct {
	Schema    int    `json:"schema"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	IsFromMe  bool   `json:"is_from_me"`
	Chat      Chat   `json:"chat"`
	Sender    Sender `json:"sender"`
}

type Media struct {
	Kind     string  `json:"kind"`
	Mime     *string `json:"mime"`
	Size     *int64  `json:"size"`
	SHA256   *string `json:"sha256"`
	Filename *string `json:"filename"`
	Duration *int    `json:"duration"`
	PTT      bool    `json:"ptt"`
	Width    *int    `json:"width"`
	Height   *int    `json:"height"`
	URL      string  `json:"url"`
}

type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      *string `json:"name"`
	Address   *string `json:"address"`
}

type Contact struct {
	Name   *string  `json:"name"`
	VCard  *string  `json:"vcard"`
	Phones []string `json:"phones"`
}

type Quoted struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"`
	Text   *string `json:"text"`
	Sender Party   `json:"sender"`
}

type Message struct {
	Base
	Type      string    `json:"type"`
	Text      *string   `json:"text"`
	Media     *Media    `json:"media"`
	Location  *Location `json:"location"`
	Contact   *Contact  `json:"contact"`
	Quoted    *Quoted   `json:"quoted"`
	Forwarded bool      `json:"forwarded"`
	ViewOnce  bool      `json:"view_once"`
}

type Edited struct {
	Base
	TargetID string  `json:"target_id"`
	Text     *string `json:"text"`
}

type Reaction struct {
	Base
	TargetID string  `json:"target_id"`
	Emoji    *string `json:"emoji"`
}

type Revoked struct {
	Base
	TargetID string `json:"target_id"`
}

type Ack struct {
	Base
	Status string   `json:"status"`
	IDs    []string `json:"ids"`
}
```

- [ ] **Step 4: Write `base.go`**

```go
package stablepayload

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// resolve fills pn and lid for a JID. A LID fills lid and looks up pn; a phone
// JID fills pn and looks up lid; groups, broadcasts and newsletters fill pn only.
// alt (whatsmeow's SenderAlt/RecipientAlt) is used before the resolver.
func resolve(ctx context.Context, jid, alt types.JID, r Resolver) (pn, lid *string) {
	if jid.IsEmpty() {
		return nil, nil
	}
	j := jid.ToNonAD()
	switch j.Server {
	case types.HiddenUserServer:
		lid = strPtr(j.String())
		pn = other(alt, types.DefaultUserServer, func() (types.JID, bool) {
			if r == nil {
				return types.JID{}, false
			}
			return r.PNForLID(ctx, j)
		})
	case types.DefaultUserServer:
		pn = strPtr(j.String())
		lid = other(alt, types.HiddenUserServer, func() (types.JID, bool) {
			if r == nil {
				return types.JID{}, false
			}
			return r.LIDForPN(ctx, j)
		})
	default:
		pn = strPtr(j.String())
	}
	return pn, lid
}

func other(alt types.JID, server string, lookup func() (types.JID, bool)) *string {
	if !alt.IsEmpty() && alt.Server == server {
		return strPtr(alt.ToNonAD().String())
	}
	if found, ok := lookup(); ok && !found.IsEmpty() {
		return strPtr(found.ToNonAD().String())
	}
	return nil
}

func base(ctx context.Context, info types.MessageInfo, r Resolver) Base {
	b := Base{
		Schema:    SchemaVersion,
		ID:        info.ID,
		Timestamp: info.Timestamp.UTC().Format(time.RFC3339),
		IsFromMe:  info.IsFromMe,
	}
	var chatAlt types.JID
	if info.Chat.Server != types.GroupServer {
		if info.IsFromMe {
			chatAlt = info.RecipientAlt
		} else {
			chatAlt = info.SenderAlt
		}
	}
	b.Chat.PN, b.Chat.LID = resolve(ctx, info.Chat, chatAlt, r)
	b.Chat.IsGroup = info.Chat.Server == types.GroupServer
	b.Sender.PN, b.Sender.LID = resolve(ctx, info.Sender, info.SenderAlt, r)
	b.Sender.PushName = strPtr(info.PushName)
	return b
}
```

- [ ] **Step 5: Run the tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/stablepayload/ -count=1 && GOTOOLCHAIN=auto go vet ./pkg/stablepayload/`
Expected: PASS; vet clean.

- [ ] **Step 6: Commit**

```bash
git add src/pkg/stablepayload/
git commit -m "feat(stablepayload): add the stable payload types and base fields"
```

---

### Task 2: `message` classification

**Files:**
- Create: `src/pkg/stablepayload/message.go`
- Test: `src/pkg/stablepayload/message_test.go`

**Interfaces:**
- Consumes: Task 1 types, `base`, `resolve`, `strPtr`.
- Produces:
  - `func Build(ctx context.Context, evt *events.Message, msg *waE2E.Message, r Resolver) (event string, stable any)` — `msg` is the message GOWA works on (possibly the decrypted edit); nil means `evt.Message`. Task 3 adds the edit/reaction/revoke branches inside it.
  - `func classify(msg *waE2E.Message, messageID string) (typ string, text *string, media *Media, loc *Location, contact *Contact)`
  - `func contextInfo(msg *waE2E.Message) *waE2E.ContextInfo`
  - `func unwrap(msg *waE2E.Message) *waE2E.Message`
  - test helper `newMessageEvent(raw *waE2E.Message, mods ...func(*events.Message)) *events.Message` (in `message_test.go`, reused by Tasks 3-4)

- [ ] **Step 1: Write the failing test**

Create `src/pkg/stablepayload/message_test.go`:

```go
package stablepayload

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func newMessageEvent(raw *waE2E.Message, mods ...func(*events.Message)) *events.Message {
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: pnContact, Sender: pnContact},
			ID:            "3EB0000000000000000001",
			PushName:      "Contact One",
			Timestamp:     testTime,
		},
		RawMessage: raw,
	}
	for _, mod := range mods {
		mod(evt)
	}
	return evt.UnwrapRaw()
}

func buildMessage(t *testing.T, evt *events.Message) Message {
	t.Helper()
	event, stable := Build(context.Background(), evt, nil, resolver)
	if event != EventMessage {
		t.Fatalf("event = %s, want %s", event, EventMessage)
	}
	m, ok := stable.(Message)
	if !ok {
		t.Fatalf("stable is %T, want Message", stable)
	}
	return m
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestBuildMessageTypes(t *testing.T) {
	image := &waE2E.ImageMessage{
		Caption: proto.String("look"), Mimetype: proto.String("image/jpeg"),
		FileLength: proto.Uint64(2048), FileSHA256: []byte{0xab, 0xcd},
		Width: proto.Uint32(640), Height: proto.Uint32(480),
	}
	cases := []struct {
		name     string
		raw      *waE2E.Message
		wantType string
		wantText string
		wantKind string
	}{
		{"conversation", &waE2E.Message{Conversation: proto.String("hello")}, TypeText, "hello", ""},
		{"extended text", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("hi there")}}, TypeText, "hi there", ""},
		{"image", &waE2E.Message{ImageMessage: image}, TypeImage, "look", "image"},
		{"video", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("clip"), Seconds: proto.Uint32(9)}}, TypeVideo, "clip", "video"},
		{"video note", &waE2E.Message{PtvMessage: &waE2E.VideoMessage{Seconds: proto.Uint32(4)}}, TypeVideo, "<nil>", "video"},
		{"audio", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Seconds: proto.Uint32(7)}}, TypeAudio, "<nil>", "audio"},
		{"document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("a.pdf"), Caption: proto.String("contract")}}, TypeDocument, "contract", "document"},
		{"sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{Mimetype: proto.String("image/webp")}}, TypeSticker, "<nil>", "sticker"},
		{"location", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{DegreesLatitude: proto.Float64(-23.5), DegreesLongitude: proto.Float64(-46.6), Name: proto.String("Office")}}, TypeLocation, "<nil>", ""},
		{"live location", &waE2E.Message{LiveLocationMessage: &waE2E.LiveLocationMessage{DegreesLatitude: proto.Float64(-20.3), DegreesLongitude: proto.Float64(-40.3)}}, TypeLocation, "<nil>", ""},
		{"contact", &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Ana"), Vcard: proto.String("BEGIN:VCARD\nitem1.TEL;waid=5511900000002:+55 11 90000-0002\nEND:VCARD")}}, TypeContact, "<nil>", ""},
		{"poll", &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Lunch?")}}, TypePoll, "Lunch?", ""},
		{"unknown", &waE2E.Message{ButtonsMessage: &waE2E.ButtonsMessage{ContentText: proto.String("pick")}}, TypeUnknown, "<nil>", ""},
		{"empty", &waE2E.Message{}, TypeUnknown, "<nil>", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := buildMessage(t, newMessageEvent(tc.raw))
			if m.Type != tc.wantType || deref(m.Text) != tc.wantText {
				t.Fatalf("type=%s text=%s, want type=%s text=%s", m.Type, deref(m.Text), tc.wantType, tc.wantText)
			}
			switch {
			case tc.wantKind == "" && m.Media != nil:
				t.Fatalf("media = %+v, want nil", m.Media)
			case tc.wantKind != "" && (m.Media == nil || m.Media.Kind != tc.wantKind):
				t.Fatalf("media = %+v, want kind %s", m.Media, tc.wantKind)
			case tc.wantKind != "" && m.Media.URL != "/message/3EB0000000000000000001/media":
				t.Fatalf("media.url = %s", m.Media.URL)
			}
		})
	}
}

func TestBuildMessageMediaFields(t *testing.T) {
	m := buildMessage(t, newMessageEvent(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Mimetype: proto.String("image/jpeg"), FileLength: proto.Uint64(2048), FileSHA256: []byte{0xab, 0xcd},
		Width: proto.Uint32(640), Height: proto.Uint32(480),
	}}))
	got := toJSON(t, m.Media)
	want := `{"kind":"image","mime":"image/jpeg","size":2048,"sha256":"abcd","filename":null,"duration":null,"ptt":false,"width":640,"height":480,"url":"/message/3EB0000000000000000001/media"}`
	if got != want {
		t.Fatalf("media =\n%s\nwant\n%s", got, want)
	}

	audio := buildMessage(t, newMessageEvent(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true), Seconds: proto.Uint32(7), Mimetype: proto.String("audio/ogg; codecs=opus")}}))
	if !audio.Media.PTT || audio.Media.Duration == nil || *audio.Media.Duration != 7 {
		t.Fatalf("audio media = %+v, want ptt true and duration 7", audio.Media)
	}
}

func TestBuildMessageLocationAndContact(t *testing.T) {
	loc := buildMessage(t, newMessageEvent(&waE2E.Message{LocationMessage: &waE2E.LocationMessage{
		DegreesLatitude: proto.Float64(-23.5), DegreesLongitude: proto.Float64(-46.6), Name: proto.String("Office"),
	}}))
	if got := toJSON(t, loc.Location); got != `{"latitude":-23.5,"longitude":-46.6,"name":"Office","address":null}` {
		t.Fatalf("location = %s", got)
	}

	noPhones := buildMessage(t, newMessageEvent(&waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Ana")}}))
	if got := toJSON(t, noPhones.Contact); got != `{"name":"Ana","vcard":null,"phones":[]}` {
		t.Fatalf("contact without vcard = %s, want phones []", got)
	}

	array := buildMessage(t, newMessageEvent(&waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{Contacts: []*waE2E.ContactMessage{
		{DisplayName: proto.String("First"), Vcard: proto.String("BEGIN:VCARD\nTEL;type=CELL:+55 11 90000-0003\nEND:VCARD")},
		{DisplayName: proto.String("Second")},
	}}}))
	if array.Type != TypeContact || deref(array.Contact.Name) != "First" || len(array.Contact.Phones) != 1 || array.Contact.Phones[0] != "+55 11 90000-0003" {
		t.Fatalf("contacts array = %+v", array.Contact)
	}
}

func TestBuildMessageWrappers(t *testing.T) {
	inner := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("secret")}}
	raw := &waE2E.Message{DeviceSentMessage: &waE2E.DeviceSentMessage{
		DestinationJID: proto.String(pnContact.String()),
		Message: &waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{
			ViewOnceMessageV2: &waE2E.FutureProofMessage{Message: inner},
		}}},
	}}
	m := buildMessage(t, newMessageEvent(raw, func(e *events.Message) {
		e.Info.IsFromMe = true
		e.Info.Sender = pnMe
	}))
	if m.Type != TypeImage || deref(m.Text) != "secret" || !m.ViewOnce || !m.IsFromMe {
		t.Fatalf("device-sent ephemeral view-once image: type=%s text=%s view_once=%v from_me=%v", m.Type, deref(m.Text), m.ViewOnce, m.IsFromMe)
	}
}

func TestBuildMessageQuotedAndForwarded(t *testing.T) {
	raw := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("answer"),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:      proto.String("ORIGINAL1"),
			Participant:   proto.String(lidContact.String()),
			QuotedMessage: &waE2E.Message{Conversation: proto.String("question")},
			IsForwarded:   proto.Bool(true),
		},
	}}
	m := buildMessage(t, newMessageEvent(raw))
	got := toJSON(t, m.Quoted)
	want := `{"id":"ORIGINAL1","type":"text","text":"question","sender":{"pn":"5511900000001@s.whatsapp.net","lid":"100000000000001@lid"}}`
	if got != want || !m.Forwarded {
		t.Fatalf("quoted = %s forwarded=%v, want %s forwarded=true", got, m.Forwarded, want)
	}

	plain := buildMessage(t, newMessageEvent(&waE2E.Message{Conversation: proto.String("x")}))
	if plain.Quoted != nil || plain.Forwarded {
		t.Fatalf("plain message quoted=%v forwarded=%v, want nil/false", plain.Quoted, plain.Forwarded)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/stablepayload/ -count=1`
Expected: FAIL to compile (`undefined: Build`).

- [ ] **Step 3: Write `message.go`**

```go
package stablepayload

import (
	"context"
	"encoding/hex"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Build returns the webhook event name and the stable object for a message
// event. msg is the message GOWA works on (the decrypted edit when WhatsApp
// sent a SecretEncryptedMessage); nil means evt.Message. The branch order
// matches GOWA's own event naming: protocol revoke/edit, reaction, message.
func Build(ctx context.Context, evt *events.Message, msg *waE2E.Message, r Resolver) (string, any) {
	if msg == nil {
		msg = evt.Message
	}
	msg = unwrap(msg)
	b := base(ctx, evt.Info, r)

	typ, text, media, loc, contact := classify(msg, evt.Info.ID)
	m := Message{Base: b, Type: typ, Text: text, Media: media, Location: loc, Contact: contact, ViewOnce: evt.IsViewOnce}
	if ci := contextInfo(msg); ci != nil {
		m.Forwarded = ci.GetIsForwarded()
		m.Quoted = quoted(ctx, ci, r)
	}
	return EventMessage, m
}

// unwrap removes the containers whatsmeow's UnwrapRaw may leave nested.
func unwrap(msg *waE2E.Message) *waE2E.Message {
	for i := 0; i < 5 && msg != nil; i++ {
		switch {
		case msg.GetDeviceSentMessage().GetMessage() != nil:
			msg = msg.GetDeviceSentMessage().GetMessage()
		case msg.GetEphemeralMessage().GetMessage() != nil:
			msg = msg.GetEphemeralMessage().GetMessage()
		case msg.GetViewOnceMessage().GetMessage() != nil:
			msg = msg.GetViewOnceMessage().GetMessage()
		case msg.GetViewOnceMessageV2().GetMessage() != nil:
			msg = msg.GetViewOnceMessageV2().GetMessage()
		case msg.GetViewOnceMessageV2Extension().GetMessage() != nil:
			msg = msg.GetViewOnceMessageV2Extension().GetMessage()
		case msg.GetDocumentWithCaptionMessage().GetMessage() != nil:
			msg = msg.GetDocumentWithCaptionMessage().GetMessage()
		default:
			return msg
		}
	}
	return msg
}

func classify(msg *waE2E.Message, messageID string) (string, *string, *Media, *Location, *Contact) {
	url := "/message/" + messageID + "/media"
	switch {
	case msg == nil:
		return TypeUnknown, nil, nil, nil, nil
	case msg.GetConversation() != "":
		return TypeText, strPtr(msg.GetConversation()), nil, nil, nil
	case msg.GetExtendedTextMessage() != nil:
		return TypeText, strPtr(msg.GetExtendedTextMessage().GetText()), nil, nil, nil
	case msg.GetImageMessage() != nil:
		im := msg.GetImageMessage()
		return TypeImage, strPtr(im.GetCaption()), &Media{
			Kind: "image", Mime: strPtr(im.GetMimetype()), Size: sizePtr(im.GetFileLength()), SHA256: hexPtr(im.GetFileSHA256()),
			Width: intPtr(im.GetWidth()), Height: intPtr(im.GetHeight()), URL: url,
		}, nil, nil
	case msg.GetVideoMessage() != nil || msg.GetPtvMessage() != nil:
		vm := msg.GetVideoMessage()
		if vm == nil {
			vm = msg.GetPtvMessage()
		}
		return TypeVideo, strPtr(vm.GetCaption()), &Media{
			Kind: "video", Mime: strPtr(vm.GetMimetype()), Size: sizePtr(vm.GetFileLength()), SHA256: hexPtr(vm.GetFileSHA256()),
			Duration: intPtr(vm.GetSeconds()), Width: intPtr(vm.GetWidth()), Height: intPtr(vm.GetHeight()), URL: url,
		}, nil, nil
	case msg.GetAudioMessage() != nil:
		am := msg.GetAudioMessage()
		return TypeAudio, nil, &Media{
			Kind: "audio", Mime: strPtr(am.GetMimetype()), Size: sizePtr(am.GetFileLength()), SHA256: hexPtr(am.GetFileSHA256()),
			Duration: intPtr(am.GetSeconds()), PTT: am.GetPTT(), URL: url,
		}, nil, nil
	case msg.GetDocumentMessage() != nil:
		dm := msg.GetDocumentMessage()
		return TypeDocument, strPtr(dm.GetCaption()), &Media{
			Kind: "document", Mime: strPtr(dm.GetMimetype()), Size: sizePtr(dm.GetFileLength()), SHA256: hexPtr(dm.GetFileSHA256()),
			Filename: strPtr(dm.GetFileName()), URL: url,
		}, nil, nil
	case msg.GetStickerMessage() != nil:
		sm := msg.GetStickerMessage()
		return TypeSticker, nil, &Media{
			Kind: "sticker", Mime: strPtr(sm.GetMimetype()), Size: sizePtr(sm.GetFileLength()), SHA256: hexPtr(sm.GetFileSHA256()),
			Width: intPtr(sm.GetWidth()), Height: intPtr(sm.GetHeight()), URL: url,
		}, nil, nil
	case msg.GetLocationMessage() != nil:
		lm := msg.GetLocationMessage()
		return TypeLocation, nil, nil, &Location{
			Latitude: lm.GetDegreesLatitude(), Longitude: lm.GetDegreesLongitude(),
			Name: strPtr(lm.GetName()), Address: strPtr(lm.GetAddress()),
		}, nil
	case msg.GetLiveLocationMessage() != nil:
		ll := msg.GetLiveLocationMessage()
		return TypeLocation, nil, nil, &Location{Latitude: ll.GetDegreesLatitude(), Longitude: ll.GetDegreesLongitude()}, nil
	case msg.GetContactMessage() != nil:
		return TypeContact, nil, nil, nil, contactOf(msg.GetContactMessage())
	case msg.GetContactsArrayMessage() != nil && len(msg.GetContactsArrayMessage().GetContacts()) > 0:
		// Documented limit: a contacts array keeps only its first entry.
		return TypeContact, nil, nil, nil, contactOf(msg.GetContactsArrayMessage().GetContacts()[0])
	case msg.GetPollCreationMessage() != nil:
		return TypePoll, strPtr(msg.GetPollCreationMessage().GetName()), nil, nil, nil
	case msg.GetPollCreationMessageV2() != nil:
		return TypePoll, strPtr(msg.GetPollCreationMessageV2().GetName()), nil, nil, nil
	case msg.GetPollCreationMessageV3() != nil:
		return TypePoll, strPtr(msg.GetPollCreationMessageV3().GetName()), nil, nil, nil
	}
	return TypeUnknown, nil, nil, nil, nil
}

func contactOf(c *waE2E.ContactMessage) *Contact {
	return &Contact{Name: strPtr(c.GetDisplayName()), VCard: strPtr(c.GetVcard()), Phones: vcardPhones(c.GetVcard())}
}

// vcardPhones returns the values of TEL lines ("TEL;…:+55 …" and "item1.TEL;…:…").
func vcardPhones(vcard string) []string {
	phones := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(vcard, "\r\n", "\n"), "\n") {
		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "TEL") && !strings.Contains(upper, ".TEL") {
			continue
		}
		if i := strings.LastIndex(line, ":"); i >= 0 {
			if phone := strings.TrimSpace(line[i+1:]); phone != "" {
				phones = append(phones, phone)
			}
		}
	}
	return phones
}

func contextInfo(msg *waE2E.Message) *waE2E.ContextInfo {
	for _, ci := range []*waE2E.ContextInfo{
		msg.GetExtendedTextMessage().GetContextInfo(),
		msg.GetImageMessage().GetContextInfo(),
		msg.GetVideoMessage().GetContextInfo(),
		msg.GetPtvMessage().GetContextInfo(),
		msg.GetAudioMessage().GetContextInfo(),
		msg.GetDocumentMessage().GetContextInfo(),
		msg.GetStickerMessage().GetContextInfo(),
		msg.GetLocationMessage().GetContextInfo(),
		msg.GetContactMessage().GetContextInfo(),
	} {
		if ci != nil {
			return ci
		}
	}
	return nil
}

func quoted(ctx context.Context, ci *waE2E.ContextInfo, r Resolver) *Quoted {
	if ci.GetStanzaID() == "" {
		return nil
	}
	typ, text, _, _, _ := classify(unwrap(ci.GetQuotedMessage()), ci.GetStanzaID())
	q := &Quoted{ID: ci.GetStanzaID(), Type: typ, Text: text}
	if participant, err := types.ParseJID(ci.GetParticipant()); err == nil && ci.GetParticipant() != "" {
		q.Sender.PN, q.Sender.LID = resolve(ctx, participant, types.JID{}, r)
	}
	return q
}

func sizePtr(v uint64) *int64 {
	if v == 0 {
		return nil
	}
	n := int64(v)
	return &n
}

func intPtr(v uint32) *int {
	if v == 0 {
		return nil
	}
	n := int(v)
	return &n
}

func hexPtr(b []byte) *string {
	if len(b) == 0 {
		return nil
	}
	return strPtr(hex.EncodeToString(b))
}
```

- [ ] **Step 4: Run the tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/stablepayload/ -count=1 && GOTOOLCHAIN=auto go vet ./pkg/stablepayload/`
Expected: PASS; vet clean.

- [ ] **Step 5: Commit**

```bash
git add src/pkg/stablepayload/
git commit -m "feat(stablepayload): classify message types, media, location, contact and quotes"
```

---

### Task 3: Edited, reaction, revoked and ack

**Files:**
- Modify: `src/pkg/stablepayload/message.go` (branches at the top of `Build`)
- Create: `src/pkg/stablepayload/ack.go`
- Test: `src/pkg/stablepayload/events_test.go`

**Interfaces:**
- Consumes: `Build`, `base`, `classify`, `unwrap`, `newMessageEvent` (Task 2).
- Produces: `func BuildAck(ctx context.Context, evt *events.Receipt, r Resolver) Ack`; `Build` returning `EventEdited/Edited`, `EventReaction/Reaction`, `EventRevoked/Revoked`.

- [ ] **Step 1: Write the failing test**

Create `src/pkg/stablepayload/events_test.go`:

```go
package stablepayload

import (
	"context"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestBuildEdited(t *testing.T) {
	raw := &waE2E.Message{EditedMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:           &waCommon.MessageKey{ID: proto.String("ORIGINAL1")},
		EditedMessage: &waE2E.Message{Conversation: proto.String("fixed text")},
	}}}}
	event, stable := Build(context.Background(), newMessageEvent(raw), nil, resolver)
	e, ok := stable.(Edited)
	if event != EventEdited || !ok || e.TargetID != "ORIGINAL1" || deref(e.Text) != "fixed text" {
		t.Fatalf("event=%s stable=%+v", event, stable)
	}
}

func TestBuildEditedFromDecryptedMessage(t *testing.T) {
	decrypted := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:           &waCommon.MessageKey{ID: proto.String("ORIGINAL2")},
		EditedMessage: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("new")}},
	}}
	event, stable := Build(context.Background(), newMessageEvent(&waE2E.Message{}), decrypted, resolver)
	if e, ok := stable.(Edited); event != EventEdited || !ok || e.TargetID != "ORIGINAL2" || deref(e.Text) != "new" {
		t.Fatalf("event=%s stable=%+v", event, stable)
	}
}

func TestBuildReaction(t *testing.T) {
	react := func(text string) *waE2E.Message {
		return &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: &waCommon.MessageKey{ID: proto.String("TARGET1")}, Text: proto.String(text)}}
	}
	event, stable := Build(context.Background(), newMessageEvent(react("👍")), nil, resolver)
	if r, ok := stable.(Reaction); event != EventReaction || !ok || r.TargetID != "TARGET1" || deref(r.Emoji) != "👍" {
		t.Fatalf("event=%s stable=%+v", event, stable)
	}
	_, removed := Build(context.Background(), newMessageEvent(react("")), nil, resolver)
	if got := toJSON(t, removed); !strings.Contains(got, `"emoji":null`) {
		t.Fatalf("removed reaction = %s, want emoji null", got)
	}
}

func TestBuildRevoked(t *testing.T) {
	raw := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key:  &waCommon.MessageKey{ID: proto.String("GONE1")},
	}}
	event, stable := Build(context.Background(), newMessageEvent(raw), nil, resolver)
	if r, ok := stable.(Revoked); event != EventRevoked || !ok || r.TargetID != "GONE1" {
		t.Fatalf("event=%s stable=%+v", event, stable)
	}
}

func TestBuildOtherProtocolMessageIsUnknownMessage(t *testing.T) {
	raw := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum()}}
	event, stable := Build(context.Background(), newMessageEvent(raw), nil, resolver)
	if m, ok := stable.(Message); event != EventMessage || !ok || m.Type != TypeUnknown {
		t.Fatalf("event=%s stable=%+v", event, stable)
	}
}

func TestBuildAck(t *testing.T) {
	evt := &events.Receipt{
		MessageSource: types.MessageSource{Chat: pnContact, Sender: pnContact},
		MessageIDs:    []types.MessageID{"A1", "A2"},
		Timestamp:     testTime,
		Type:          types.ReceiptTypeRead,
	}
	got := toJSON(t, BuildAck(context.Background(), evt, resolver))
	want := `{"schema":1,"id":"A1","timestamp":"2026-09-25T12:00:00Z","is_from_me":false,` +
		`"chat":{"pn":"5511900000001@s.whatsapp.net","lid":"100000000000001@lid","is_group":false},` +
		`"sender":{"pn":"5511900000001@s.whatsapp.net","lid":"100000000000001@lid","push_name":null},` +
		`"status":"read","ids":["A1","A2"]}`
	if got != want {
		t.Fatalf("ack =\n%s\nwant\n%s", got, want)
	}

	for rt, want := range map[types.ReceiptType]string{
		types.ReceiptTypeDelivered: "delivered", types.ReceiptTypeReadSelf: "read",
		types.ReceiptTypePlayed: "played", types.ReceiptTypePlayedSelf: "played",
	} {
		if got := BuildAck(context.Background(), &events.Receipt{Type: rt, MessageIDs: []types.MessageID{"X"}}, nil).Status; got != want {
			t.Errorf("status for %q = %s, want %s", rt, got, want)
		}
	}
	if got := toJSON(t, BuildAck(context.Background(), &events.Receipt{}, nil)); !strings.Contains(got, `"ids":[]`) {
		t.Fatalf("empty ack = %s, want ids []", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/stablepayload/ -count=1`
Expected: FAIL to compile (`undefined: BuildAck`).

- [ ] **Step 3: Add the branches to `Build`**

In `message.go`, right after `b := base(ctx, evt.Info, r)` insert:

```go
	if pm := msg.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			return EventRevoked, Revoked{Base: b, TargetID: pm.GetKey().GetID()}
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			_, text, _, _, _ := classify(unwrap(pm.GetEditedMessage()), evt.Info.ID)
			return EventEdited, Edited{Base: b, TargetID: pm.GetKey().GetID(), Text: text}
		}
	}
	if rm := msg.GetReactionMessage(); rm != nil {
		return EventReaction, Reaction{Base: b, TargetID: rm.GetKey().GetID(), Emoji: strPtr(rm.GetText())}
	}
```

- [ ] **Step 4: Write `ack.go`**

```go
package stablepayload

import (
	"context"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// BuildAck returns the stable object of a message.ack event. The base id is
// the first acknowledged message id; ids lists all of them.
func BuildAck(ctx context.Context, evt *events.Receipt, r Resolver) Ack {
	ids := make([]string, 0, len(evt.MessageIDs))
	for _, id := range evt.MessageIDs {
		ids = append(ids, string(id))
	}
	first := ""
	if len(ids) > 0 {
		first = ids[0]
	}
	b := base(ctx, types.MessageInfo{MessageSource: evt.MessageSource, ID: first, Timestamp: evt.Timestamp}, r)
	return Ack{Base: b, Status: ackStatus(evt.Type), IDs: ids}
}

func ackStatus(t types.ReceiptType) string {
	switch t {
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
		return "read"
	case types.ReceiptTypePlayed, types.ReceiptTypePlayedSelf:
		return "played"
	default:
		return "delivered"
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/stablepayload/ -count=1 && GOTOOLCHAIN=auto go vet ./pkg/stablepayload/`
Expected: PASS; vet clean.

- [ ] **Step 6: Commit**

```bash
git add src/pkg/stablepayload/
git commit -m "feat(stablepayload): build edited, reaction, revoked and ack payloads"
```

---

### Task 4: Synthetic golden fixtures

**Files:**
- Create: `src/pkg/stablepayload/golden_test.go`
- Create (generated): `contract/fixtures/synthetic/*.json`

**Interfaces:**
- Consumes: `Build`, `BuildAck`, `newMessageEvent`, `resolver`, `pnMe`, `groupJID`, `lidContact`, `testTime` (Tasks 1-3).
- Produces: fixture files named `<case>.json` in `contract/fixtures/synthetic/`, format `{"event":…,"stable":…}`; the `-update` flag of this test rewrites them.

- [ ] **Step 1: Write the golden test**

Create `src/pkg/stablepayload/golden_test.go`:

```go
package stablepayload

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

var update = flag.Bool("update", false, "rewrite contract/fixtures/synthetic")

var fixtureDir = filepath.Join("..", "..", "..", "contract", "fixtures", "synthetic")

func goldenCases() map[string]func() (string, any) {
	ctx := context.Background()
	msg := func(raw *waE2E.Message, mods ...func(*events.Message)) func() (string, any) {
		return func() (string, any) { return Build(ctx, newMessageEvent(raw, mods...), nil, resolver) }
	}
	group := func(e *events.Message) {
		e.Info.Chat = groupJID
		e.Info.IsGroup = true
		e.Info.Sender = lidContact
	}
	fromMe := func(e *events.Message) {
		e.Info.IsFromMe = true
		e.Info.Sender = pnMe
		e.Info.PushName = ""
	}
	ack := func(rt types.ReceiptType) func() (string, any) {
		return func() (string, any) {
			return EventAck, BuildAck(ctx, &events.Receipt{
				MessageSource: types.MessageSource{Chat: pnContact, Sender: pnContact},
				MessageIDs:    []types.MessageID{"3EB0000000000000000001"}, Timestamp: testTime, Type: rt,
			}, resolver)
		}
	}
	key := &waCommon.MessageKey{ID: proto.String("3EB0000000000000000000")}
	return map[string]func() (string, any){
		"message-text":             msg(&waE2E.Message{Conversation: proto.String("hello")}),
		"message-text-extended":    msg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("hi there")}}),
		"message-image":            msg(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("look"), Mimetype: proto.String("image/jpeg"), FileLength: proto.Uint64(2048), FileSHA256: []byte{0xab, 0xcd}, Width: proto.Uint32(640), Height: proto.Uint32(480)}}),
		"message-video":            msg(&waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("clip"), Mimetype: proto.String("video/mp4"), FileLength: proto.Uint64(4096), Seconds: proto.Uint32(9)}}),
		"message-video-note":       msg(&waE2E.Message{PtvMessage: &waE2E.VideoMessage{Mimetype: proto.String("video/mp4"), Seconds: proto.Uint32(4)}}),
		"message-audio":            msg(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String("audio/mpeg"), Seconds: proto.Uint32(30)}}),
		"message-audio-ptt":        msg(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String("audio/ogg; codecs=opus"), Seconds: proto.Uint32(7), PTT: proto.Bool(true)}}),
		"message-document":         msg(&waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("contract.pdf"), Caption: proto.String("please sign"), Mimetype: proto.String("application/pdf"), FileLength: proto.Uint64(9000)}}}}),
		"message-sticker":          msg(&waE2E.Message{StickerMessage: &waE2E.StickerMessage{Mimetype: proto.String("image/webp"), Width: proto.Uint32(512), Height: proto.Uint32(512)}}),
		"message-location":         msg(&waE2E.Message{LocationMessage: &waE2E.LocationMessage{DegreesLatitude: proto.Float64(-23.5), DegreesLongitude: proto.Float64(-46.6), Name: proto.String("Office"), Address: proto.String("Main St 1")}}),
		"message-live-location":    msg(&waE2E.Message{LiveLocationMessage: &waE2E.LiveLocationMessage{DegreesLatitude: proto.Float64(-20.3), DegreesLongitude: proto.Float64(-40.3)}}),
		"message-contact":          msg(&waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Ana"), Vcard: proto.String("BEGIN:VCARD\nitem1.TEL;waid=5511900000002:+55 11 90000-0002\nEND:VCARD")}}),
		"message-contacts-array":   msg(&waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{Contacts: []*waE2E.ContactMessage{{DisplayName: proto.String("First")}, {DisplayName: proto.String("Second")}}}}),
		"message-poll":             msg(&waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Lunch?")}}),
		"message-unknown":          msg(&waE2E.Message{ButtonsMessage: &waE2E.ButtonsMessage{ContentText: proto.String("pick")}}),
		"message-ephemeral":        msg(&waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{Conversation: proto.String("disappearing")}}}),
		"message-view-once":        msg(&waE2E.Message{ViewOnceMessageV2: &waE2E.FutureProofMessage{Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg")}}}}),
		"message-device-sent":      msg(&waE2E.Message{DeviceSentMessage: &waE2E.DeviceSentMessage{DestinationJID: proto.String(pnContact.String()), Message: &waE2E.Message{Conversation: proto.String("sent from phone")}}}, fromMe),
		"message-group":            msg(&waE2E.Message{Conversation: proto.String("hi group")}, group),
		"message-lid-only":         func() (string, any) { return Build(ctx, newMessageEvent(&waE2E.Message{Conversation: proto.String("who?")}, func(e *events.Message) { e.Info.Chat = lidContact; e.Info.Sender = lidContact }), nil, fakeResolver{}) },
		"message-reply":            msg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("answer"), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("3EB0000000000000000000"), Participant: proto.String(lidContact.String()), QuotedMessage: &waE2E.Message{Conversation: proto.String("question")}}}}),
		"message-forwarded":        msg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("fwd"), ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(true)}}}),
		"message-edited":           msg(&waE2E.Message{EditedMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: key, EditedMessage: &waE2E.Message{Conversation: proto.String("fixed")}}}}}),
		"message-reaction":         msg(&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: key, Text: proto.String("👍")}}),
		"message-reaction-removed": msg(&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: key, Text: proto.String("")}}),
		"message-revoked":          msg(&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: key}}),
		"message-ack-delivered":    ack(types.ReceiptTypeDelivered),
		"message-ack-read":         ack(types.ReceiptTypeRead),
	}
}

func TestGoldenFixtures(t *testing.T) {
	if *update {
		if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, build := range goldenCases() {
		t.Run(name, func(t *testing.T) {
			event, stable := build()
			got, err := json.MarshalIndent(map[string]any{"event": event, "stable": stable}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join(fixtureDir, name+".json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing fixture %s (run with -update): %v", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("fixture %s drifted; rerun with -update if intended\n--- got\n%s", path, got)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/stablepayload/ -run TestGoldenFixtures -count=1`
Expected: FAIL with `missing fixture ../../../contract/fixtures/synthetic/…json (run with -update)` for every case.

- [ ] **Step 3: Generate, inspect, re-run**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/stablepayload/ -run TestGoldenFixtures -count=1 -update && ls ../contract/fixtures/synthetic | wc -l && cat ../contract/fixtures/synthetic/message-reply.json && GOTOOLCHAIN=auto go test ./pkg/stablepayload/ -count=1`
Expected: `28` files; `message-reply.json` shows `"quoted": {"id": "3EB0000000000000000000", "type": "text", "text": "question", "sender": {…}}`; the full package PASSES.

- [ ] **Step 4: Commit**

```bash
git add src/pkg/stablepayload/golden_test.go contract/fixtures/synthetic/
git commit -m "test(stablepayload): generate synthetic contract fixtures"
```

---

### Task 5: Contract module: schema and validation

**Files:**
- Create: `contract/go.mod` (and `contract/go.sum` via `go mod tidy`)
- Create: `contract/stable.schema.json`
- Create: `contract/schema_test.go`
- Modify: `.github/workflows/elphant-ci.yml` (add a contract step)

**Interfaces:**
- Consumes: `contract/fixtures/synthetic/*.json` (Task 4).
- Produces: schema file `contract/stable.schema.json`; Go module path `github.com/eduardoelphant/go-whatsapp-web-multidevice/contract`; test helpers `compileSchema(t)` and `loadJSON(t, path)` in package `contract`.

- [ ] **Step 1: Write the failing test**

Create `contract/go.mod`:

```
module github.com/eduardoelphant/go-whatsapp-web-multidevice/contract

go 1.22
```

Create `contract/schema_test.go`:

```go
package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	schema, err := c.Compile("stable.schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return schema
}

func loadJSON(t *testing.T, path string) any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func TestFixturesMatchSchema(t *testing.T) {
	schema := compileSchema(t)
	files, _ := filepath.Glob(filepath.Join("fixtures", "*", "*.json"))
	synthetic := 0
	for _, f := range files {
		if strings.Contains(f, "synthetic") {
			synthetic++
		}
		t.Run(f, func(t *testing.T) {
			if err := schema.Validate(loadJSON(t, f)); err != nil {
				t.Errorf("%v", err)
			}
		})
	}
	if synthetic == 0 {
		t.Fatal("no synthetic fixtures found")
	}
}

func TestSchemaRejectsContractViolations(t *testing.T) {
	schema := compileSchema(t)
	mutate := func(fn func(doc map[string]any, stable map[string]any)) any {
		doc := loadJSON(t, filepath.Join("fixtures", "synthetic", "message-image.json")).(map[string]any)
		raw, _ := json.Marshal(doc)
		var copyDoc map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		_ = dec.Decode(&copyDoc)
		fn(copyDoc, copyDoc["stable"].(map[string]any))
		return copyDoc
	}
	cases := map[string]any{
		"missing chat.lid": mutate(func(_, s map[string]any) { delete(s["chat"].(map[string]any), "lid") }),
		"text is a number": mutate(func(_, s map[string]any) { s["text"] = json.Number("5") }),
		"unknown key":      mutate(func(_, s map[string]any) { s["extra"] = true }),
		"unknown type":     mutate(func(_, s map[string]any) { s["type"] = "gif" }),
		"missing media":    mutate(func(_, s map[string]any) { delete(s, "media") }),
		"bad media url":    mutate(func(_, s map[string]any) { s["media"].(map[string]any)["url"] = "https://x/y" }),
		"unknown event":    mutate(func(d, _ map[string]any) { d["event"] = "message.deleted" }),
		"schema 2":         mutate(func(_, s map[string]any) { s["schema"] = json.Number("2") }),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := schema.Validate(doc); err == nil {
				t.Fatal("schema accepted a contract violation")
			}
		})
	}
}
```

Run: `cd contract && GOTOOLCHAIN=auto go get github.com/santhosh-tekuri/jsonschema/v6 && GOTOOLCHAIN=auto go mod tidy && GOTOOLCHAIN=auto go test ./... -count=1`
Expected: FAIL: `compile schema: … stable.schema.json … no such file`.

- [ ] **Step 2: Write `contract/stable.schema.json`**

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "GOWA fork payload.stable, schema 1",
  "type": "object",
  "required": ["event", "stable"],
  "additionalProperties": false,
  "properties": {
    "event": { "enum": ["message", "message.edited", "message.reaction", "message.revoked", "message.ack"] },
    "stable": { "type": "object" }
  },
  "allOf": [
    { "if": { "properties": { "event": { "const": "message" } } }, "then": { "properties": { "stable": { "$ref": "#/$defs/message" } } } },
    { "if": { "properties": { "event": { "const": "message.edited" } } }, "then": { "properties": { "stable": { "$ref": "#/$defs/edited" } } } },
    { "if": { "properties": { "event": { "const": "message.reaction" } } }, "then": { "properties": { "stable": { "$ref": "#/$defs/reaction" } } } },
    { "if": { "properties": { "event": { "const": "message.revoked" } } }, "then": { "properties": { "stable": { "$ref": "#/$defs/revoked" } } } },
    { "if": { "properties": { "event": { "const": "message.ack" } } }, "then": { "properties": { "stable": { "$ref": "#/$defs/ack" } } } }
  ],
  "$defs": {
    "nstr": { "type": ["string", "null"] },
    "nint": { "type": ["integer", "null"] },
    "jid": { "type": ["string", "null"], "pattern": "^[^@]+@[a-z.]+$" },
    "messageType": { "enum": ["text", "image", "video", "audio", "document", "sticker", "location", "contact", "poll", "unknown"] },
    "party": {
      "type": "object", "additionalProperties": false, "required": ["pn", "lid"],
      "properties": { "pn": { "$ref": "#/$defs/jid" }, "lid": { "$ref": "#/$defs/jid" } }
    },
    "base": {
      "required": ["schema", "id", "timestamp", "is_from_me", "chat", "sender"],
      "properties": {
        "schema": { "const": 1 },
        "id": { "type": "string" },
        "timestamp": { "type": "string", "format": "date-time" },
        "is_from_me": { "type": "boolean" },
        "chat": {
          "type": "object", "additionalProperties": false, "required": ["pn", "lid", "is_group"],
          "properties": { "pn": { "$ref": "#/$defs/jid" }, "lid": { "$ref": "#/$defs/jid" }, "is_group": { "type": "boolean" } }
        },
        "sender": {
          "type": "object", "additionalProperties": false, "required": ["pn", "lid", "push_name"],
          "properties": { "pn": { "$ref": "#/$defs/jid" }, "lid": { "$ref": "#/$defs/jid" }, "push_name": { "$ref": "#/$defs/nstr" } }
        }
      }
    },
    "media": {
      "type": "object", "additionalProperties": false,
      "required": ["kind", "mime", "size", "sha256", "filename", "duration", "ptt", "width", "height", "url"],
      "properties": {
        "kind": { "enum": ["image", "video", "audio", "document", "sticker"] },
        "mime": { "$ref": "#/$defs/nstr" },
        "size": { "$ref": "#/$defs/nint" },
        "sha256": { "$ref": "#/$defs/nstr" },
        "filename": { "$ref": "#/$defs/nstr" },
        "duration": { "$ref": "#/$defs/nint" },
        "ptt": { "type": "boolean" },
        "width": { "$ref": "#/$defs/nint" },
        "height": { "$ref": "#/$defs/nint" },
        "url": { "type": "string", "pattern": "^/message/[^/]+/media$" }
      }
    },
    "location": {
      "type": "object", "additionalProperties": false, "required": ["latitude", "longitude", "name", "address"],
      "properties": { "latitude": { "type": "number" }, "longitude": { "type": "number" }, "name": { "$ref": "#/$defs/nstr" }, "address": { "$ref": "#/$defs/nstr" } }
    },
    "contact": {
      "type": "object", "additionalProperties": false, "required": ["name", "vcard", "phones"],
      "properties": { "name": { "$ref": "#/$defs/nstr" }, "vcard": { "$ref": "#/$defs/nstr" }, "phones": { "type": "array", "items": { "type": "string" } } }
    },
    "quoted": {
      "type": "object", "additionalProperties": false, "required": ["id", "type", "text", "sender"],
      "properties": { "id": { "type": "string" }, "type": { "$ref": "#/$defs/messageType" }, "text": { "$ref": "#/$defs/nstr" }, "sender": { "$ref": "#/$defs/party" } }
    },
    "message": {
      "type": "object", "allOf": [{ "$ref": "#/$defs/base" }], "unevaluatedProperties": false,
      "required": ["type", "text", "media", "location", "contact", "quoted", "forwarded", "view_once"],
      "properties": {
        "type": { "$ref": "#/$defs/messageType" },
        "text": { "$ref": "#/$defs/nstr" },
        "media": { "anyOf": [{ "type": "null" }, { "$ref": "#/$defs/media" }] },
        "location": { "anyOf": [{ "type": "null" }, { "$ref": "#/$defs/location" }] },
        "contact": { "anyOf": [{ "type": "null" }, { "$ref": "#/$defs/contact" }] },
        "quoted": { "anyOf": [{ "type": "null" }, { "$ref": "#/$defs/quoted" }] },
        "forwarded": { "type": "boolean" },
        "view_once": { "type": "boolean" }
      }
    },
    "edited": {
      "type": "object", "allOf": [{ "$ref": "#/$defs/base" }], "unevaluatedProperties": false,
      "required": ["target_id", "text"],
      "properties": { "target_id": { "type": "string" }, "text": { "$ref": "#/$defs/nstr" } }
    },
    "reaction": {
      "type": "object", "allOf": [{ "$ref": "#/$defs/base" }], "unevaluatedProperties": false,
      "required": ["target_id", "emoji"],
      "properties": { "target_id": { "type": "string" }, "emoji": { "$ref": "#/$defs/nstr" } }
    },
    "revoked": {
      "type": "object", "allOf": [{ "$ref": "#/$defs/base" }], "unevaluatedProperties": false,
      "required": ["target_id"],
      "properties": { "target_id": { "type": "string" } }
    },
    "ack": {
      "type": "object", "allOf": [{ "$ref": "#/$defs/base" }], "unevaluatedProperties": false,
      "required": ["status", "ids"],
      "properties": { "status": { "enum": ["delivered", "read", "played"] }, "ids": { "type": "array", "minItems": 1, "items": { "type": "string" } } }
    }
  }
}
```

- [ ] **Step 3: Run the contract tests**

Run: `cd contract && GOTOOLCHAIN=auto go test ./... -count=1`
Expected: PASS: 28 fixtures valid; all 8 violations rejected.

- [ ] **Step 4: Add the contract step to CI**

In `.github/workflows/elphant-ci.yml`, after the `Test` step, add:

```yaml
      - name: Contract tests
        working-directory: contract
        run: go test ./...
```

Run: `cd src && GOTOOLCHAIN=auto go run github.com/rhysd/actionlint/cmd/actionlint@latest ../.github/workflows/elphant-*.yml && echo lint-ok`
Expected: `lint-ok`.

- [ ] **Step 5: Commit**

```bash
git add contract/go.mod contract/go.sum contract/stable.schema.json contract/schema_test.go .github/workflows/elphant-ci.yml
git commit -m "test(contract): add the stable payload JSON Schema and fixture validation"
```

---

### Task 6: Shape signature and `compare`

**Files:**
- Create: `contract/shape/shape.go`
- Test: `contract/shape/shape_test.go`
- Create: `contract/cmd/compare/main.go`

**Interfaces:**
- Produces:
  - `func Signature(event string, stable map[string]any) string` — `event|type|comma-joined sorted non-null paths` (array elements collapse to `[]`)
  - `func Kinds(stable map[string]any) map[string]string` — path → `string|number|boolean|object|array|null`
  - `type Fixture struct { File, Event string; Stable map[string]any }`
  - `type Report struct { Missing, KindMismatches []string }`
  - `func Compare(synthetic, real []Fixture) Report`
  - `func Load(dir string) ([]Fixture, error)`
  - command: `cd contract && go run ./cmd/compare` (exit 1 when the report is not empty)

- [ ] **Step 1: Write the failing test**

Create `contract/shape/shape_test.go`:

```go
package shape

import (
	"strings"
	"testing"
)

func stable(text any, media any) map[string]any {
	return map[string]any{"type": "image", "text": text, "media": media, "chat": map[string]any{"pn": "x@s.whatsapp.net", "lid": nil}}
}

func TestSignatureIgnoresValuesButNotNullness(t *testing.T) {
	a := Signature("message", stable("one", map[string]any{"kind": "image"}))
	b := Signature("message", stable("two", map[string]any{"kind": "image"}))
	c := Signature("message", stable(nil, map[string]any{"kind": "image"}))
	if a != b {
		t.Fatalf("same shape, different values: %q vs %q", a, b)
	}
	if a == c {
		t.Fatal("text null vs string must change the signature")
	}
	if !strings.HasPrefix(a, "message|image|") || strings.Contains(a, "chat.lid") {
		t.Fatalf("signature = %q", a)
	}
}

func TestCompareReportsMissingShapesAndKindMismatches(t *testing.T) {
	synthetic := []Fixture{{File: "s1.json", Event: "message", Stable: stable("x", map[string]any{"kind": "image", "size": 1.0})}}
	real := []Fixture{
		{File: "r1.json", Event: "message", Stable: stable("y", map[string]any{"kind": "image", "size": 2.0})},
		{File: "r2.json", Event: "message", Stable: stable(nil, map[string]any{"kind": "image", "size": "big"})},
	}
	report := Compare(synthetic, real)
	if len(report.Missing) != 1 || !strings.Contains(report.Missing[0], "r2.json") {
		t.Fatalf("missing = %v, want only r2.json", report.Missing)
	}
	if len(report.KindMismatches) != 1 || !strings.Contains(report.KindMismatches[0], "media.size: real=string synthetic=number") {
		t.Fatalf("kind mismatches = %v", report.KindMismatches)
	}
}
```

Run: `cd contract && GOTOOLCHAIN=auto go test ./shape/ -count=1`
Expected: FAIL to compile (`undefined: Signature`).

- [ ] **Step 2: Write `contract/shape/shape.go`**

```go
// Package shape compares the structure of stable payload fixtures.
package shape

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Fixture struct {
	File   string
	Event  string
	Stable map[string]any
}

type Report struct {
	Missing        []string
	KindMismatches []string
}

func walk(prefix string, v any, visit func(path string, value any)) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			visit(p, child)
			walk(p, child, visit)
		}
	case []any:
		for _, child := range x {
			visit(prefix+"[]", child)
			walk(prefix+"[]", child, visit)
		}
	}
}

// Signature identifies a shape: event, message type and the sorted set of
// paths holding non-null values.
func Signature(event string, stable map[string]any) string {
	typ, _ := stable["type"].(string)
	seen := map[string]bool{}
	walk("", stable, func(p string, v any) {
		if v != nil {
			seen[p] = true
		}
	})
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return event + "|" + typ + "|" + strings.Join(paths, ",")
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, json.Number:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return fmt.Sprintf("%T", v)
}

func Kinds(stable map[string]any) map[string]string {
	kinds := map[string]string{}
	walk("", stable, func(p string, v any) {
		if k := kindOf(v); k != "null" {
			kinds[p] = k
		}
	})
	return kinds
}

func Compare(synthetic, real []Fixture) Report {
	var report Report
	synSigs := map[string]bool{}
	byGroup := map[string][]Fixture{}
	for _, f := range synthetic {
		synSigs[Signature(f.Event, f.Stable)] = true
		typ, _ := f.Stable["type"].(string)
		byGroup[f.Event+"|"+typ] = append(byGroup[f.Event+"|"+typ], f)
	}
	seen := map[string]bool{}
	for _, r := range real {
		sig := Signature(r.Event, r.Stable)
		if !synSigs[sig] {
			report.Missing = append(report.Missing, fmt.Sprintf("real shape without synthetic fixture: %s (%s)", sig, r.File))
		}
		typ, _ := r.Stable["type"].(string)
		group := r.Event + "|" + typ
		realKinds := Kinds(r.Stable)
		for _, s := range byGroup[group] {
			for path, sk := range Kinds(s.Stable) {
				if rk, ok := realKinds[path]; ok && rk != sk {
					line := fmt.Sprintf("%s %s: real=%s synthetic=%s (%s vs %s)", group, path, rk, sk, r.File, s.File)
					if !seen[line] {
						seen[line] = true
						report.KindMismatches = append(report.KindMismatches, line)
					}
				}
			}
		}
	}
	sort.Strings(report.Missing)
	sort.Strings(report.KindMismatches)
	return report
}

// Load reads every {"event","stable"} fixture in dir.
func Load(dir string) ([]Fixture, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	fixtures := make([]Fixture, 0, len(files))
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Event  string         `json:"event"`
			Stable map[string]any `json:"stable"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		fixtures = append(fixtures, Fixture{File: filepath.Base(file), Event: doc.Event, Stable: doc.Stable})
	}
	return fixtures, nil
}
```

- [ ] **Step 3: Write `contract/cmd/compare/main.go`**

```go
// Command compare reports real stable payload shapes that have no synthetic
// fixture, and paths whose JSON kind differs between real and synthetic.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/eduardoelphant/go-whatsapp-web-multidevice/contract/shape"
)

func main() {
	dir := flag.String("dir", "fixtures", "fixtures root with synthetic/ and real/")
	flag.Parse()
	synthetic, err := shape.Load(filepath.Join(*dir, "synthetic"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	real, err := shape.Load(filepath.Join(*dir, "real"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	report := shape.Compare(synthetic, real)
	for _, line := range report.Missing {
		fmt.Println("MISSING", line)
	}
	for _, line := range report.KindMismatches {
		fmt.Println("KIND", line)
	}
	fmt.Printf("synthetic=%d real=%d missing=%d kind_mismatches=%d\n", len(synthetic), len(real), len(report.Missing), len(report.KindMismatches))
	if len(report.Missing)+len(report.KindMismatches) > 0 {
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Run tests and the command**

Run: `cd contract && GOTOOLCHAIN=auto go test ./... -count=1 && GOTOOLCHAIN=auto go run ./cmd/compare`
Expected: PASS; the command prints `synthetic=28 real=0 missing=0 kind_mismatches=0` and exits 0.

- [ ] **Step 5: Commit**

```bash
git add contract/shape/ contract/cmd/
git commit -m "feat(contract): add shape signatures and the compare command"
```

---

### Task 7: Hooks in GOWA

**Files:**
- Create: `src/infrastructure/whatsapp/stable_payload.go`
- Modify: `src/infrastructure/whatsapp/event_message.go` (one hook line after the SecretEncryptedMessage block in `buildEventPayload`)
- Modify: `src/infrastructure/whatsapp/event_receipt.go` (one hook line in `forwardReceiptToWebhook`)
- Test: `src/infrastructure/whatsapp/stable_payload_test.go`

**Interfaces:**
- Consumes: `stablepayload.Build`, `stablepayload.BuildAck`, `stablepayload.Resolver`.
- Produces: `func addStablePayload(ctx context.Context, client *whatsmeow.Client, evt *events.Message, msg *waE2E.Message, payload map[string]any)`; `func addStableAck(ctx context.Context, client *whatsmeow.Client, evt *events.Receipt, body map[string]any)`; `func auditStable(event string, stable any)` is called here and defined in Task 8 (Task 7 adds a no-op placeholder body that Task 8 replaces: `func auditStable(string, any) {}` in `stable_payload.go`, removed in Task 8 Step 3).

- [ ] **Step 1: Write the failing test**

Create `src/infrastructure/whatsapp/stable_payload_test.go`:

```go
package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/stablepayload"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestBuildEventPayloadAddsStable(t *testing.T) {
	jid := types.NewJID("5511900000001", types.DefaultUserServer)
	evt := (&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: jid, Sender: jid},
			ID:            "STABLE1", Timestamp: time.Now(),
		},
		RawMessage: &waE2E.Message{Conversation: proto.String("hello")},
	}).UnwrapRaw()

	event, payload, err := buildEventPayload(context.Background(), nil, evt)
	if err != nil {
		t.Fatal(err)
	}
	stable, ok := payload["stable"].(stablepayload.Message)
	if event != "message" || !ok || stable.Type != stablepayload.TypeText || stable.ID != "STABLE1" {
		t.Fatalf("event=%s stable=%#v", event, payload["stable"])
	}
	if payload["body"] != "hello" {
		t.Fatalf("legacy body = %v, want hello (existing fields must stay)", payload["body"])
	}
}

func TestAddStableAck(t *testing.T) {
	jid := types.NewJID("5511900000001", types.DefaultUserServer)
	evt := &events.Receipt{
		MessageSource: types.MessageSource{Chat: jid, Sender: jid},
		MessageIDs:    []types.MessageID{"ACK1"}, Timestamp: time.Now(), Type: types.ReceiptTypeRead,
	}
	body := createReceiptPayload(context.Background(), evt, "", nil)
	addStableAck(context.Background(), nil, evt, body)
	inner := body["payload"].(map[string]any)
	ack, ok := inner["stable"].(stablepayload.Ack)
	if !ok || ack.Status != "read" || len(ack.IDs) != 1 || ack.IDs[0] != "ACK1" {
		t.Fatalf("stable = %#v", inner["stable"])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'AddsStable|AddStableAck' -count=1`
Expected: FAIL to compile (`undefined: addStableAck`).

- [ ] **Step 3: Write `stable_payload.go`**

```go
package whatsapp

import (
	"context"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/stablepayload"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Fork (elphant): payload.stable, the closed and versioned view of message
// webhooks. Contract: contract/stable.schema.json; docs/elphant-fork.md.

type stableResolver struct{ client *whatsmeow.Client }

func newStableResolver(client *whatsmeow.Client) stablepayload.Resolver {
	if client == nil || client.Store == nil || client.Store.LIDs == nil {
		return nil
	}
	return stableResolver{client: client}
}

func (r stableResolver) PNForLID(ctx context.Context, lid types.JID) (types.JID, bool) {
	pn, err := r.client.Store.LIDs.GetPNForLID(ctx, lid)
	return pn, err == nil && !pn.IsEmpty()
}

func (r stableResolver) LIDForPN(ctx context.Context, pn types.JID) (types.JID, bool) {
	lid, err := r.client.Store.LIDs.GetLIDForPN(ctx, pn)
	return lid, err == nil && !lid.IsEmpty()
}

// addStablePayload sets payload["stable"] for a message event. msg is the
// message buildEventPayload works on (the decrypted edit when applicable).
func addStablePayload(ctx context.Context, client *whatsmeow.Client, evt *events.Message, msg *waE2E.Message, payload map[string]any) {
	event, stable := stablepayload.Build(ctx, evt, msg, newStableResolver(client))
	payload["stable"] = stable
	auditStable(event, stable)
}

// addStableAck sets payload.stable on a message.ack webhook body.
func addStableAck(ctx context.Context, client *whatsmeow.Client, evt *events.Receipt, body map[string]any) {
	inner, ok := body["payload"].(map[string]any)
	if !ok {
		return
	}
	stable := stablepayload.BuildAck(ctx, evt, newStableResolver(client))
	inner["stable"] = stable
	auditStable(stablepayload.EventAck, stable)
}

// auditStable is replaced by the real audit in stable_audit.go (Task 8).
func auditStable(string, any) {}
```

- [ ] **Step 4: Hook the upstream files**

In `event_message.go`, `buildEventPayload`, right after the closing `}` of the `if sem := msg.GetSecretEncryptedMessage(); …` block, insert:

```go

	// Fork (elphant): payload.stable contract. See stable_payload.go.
	addStablePayload(ctx, client, evt, msg, payload)
```

In `event_receipt.go`, `forwardReceiptToWebhook`, replace:

```go
	payload := createReceiptPayload(ctx, evt, deviceID, client)
	return forwardPayloadToConfiguredWebhooks(ctx, payload, "message.ack")
```

with:

```go
	payload := createReceiptPayload(ctx, evt, deviceID, client)
	// Fork (elphant): payload.stable contract. See stable_payload.go.
	addStableAck(ctx, client, evt, payload)
	return forwardPayloadToConfiguredWebhooks(ctx, payload, "message.ack")
```

- [ ] **Step 5: Run the package and the suite**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ ./pkg/... -count=1 && GOTOOLCHAIN=auto go vet ./... && GOTOOLCHAIN=auto go test -race -count=1 ./infrastructure/whatsapp/`
Expected: PASS; vet clean; no data race.

- [ ] **Step 6: Commit**

```bash
git add src/infrastructure/whatsapp/stable_payload.go src/infrastructure/whatsapp/stable_payload_test.go src/infrastructure/whatsapp/event_message.go src/infrastructure/whatsapp/event_receipt.go
git commit -m "feat(whatsapp): attach payload.stable to message and ack webhooks"
```

---

### Task 8: Production audit

**Files:**
- Create: `src/infrastructure/whatsapp/stable_audit.go`
- Modify: `src/infrastructure/whatsapp/stable_payload.go` (remove the placeholder `auditStable`)
- Test: `src/infrastructure/whatsapp/stable_audit_test.go`

**Interfaces:**
- Consumes: `auditStable(event string, stable any)` call sites (Task 7).
- Produces: `func auditStable(event string, stable any)`; `func writeStableAudit(dir, event string, stable any) error`; `func anonymizeStable(v any, key string) any`; `func stableShapeKey(event string, tree map[string]any) string`; `const stableAuditCap = 3`; env var `WHATSAPP_STABLE_AUDIT_DIR`.

- [ ] **Step 1: Write the failing test**

Create `src/infrastructure/whatsapp/stable_audit_test.go`:

```go
package whatsapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleStable() map[string]any {
	return map[string]any{
		"schema": 1.0, "id": "3EB0SECRETID", "timestamp": "2026-09-25T12:00:00Z", "is_from_me": false,
		"chat":   map[string]any{"pn": "5511999998888@s.whatsapp.net", "lid": "123456789@lid", "is_group": false},
		"sender": map[string]any{"pn": "5511999998888@s.whatsapp.net", "lid": nil, "push_name": "Maria Segredo"},
		"type":   "image", "text": "senha 1234",
		"media": map[string]any{"kind": "image", "mime": "image/jpeg", "size": 2048.0, "sha256": "deadbeef",
			"filename": "rg.jpg", "duration": nil, "ptt": false, "width": 640.0, "height": 480.0, "url": "/message/3EB0SECRETID/media"},
		"contact":  map[string]any{"name": "Ana", "vcard": "BEGIN:VCARD", "phones": []any{"+55 11 90000-0002"}},
		"location": map[string]any{"latitude": -23.5, "longitude": -46.6, "name": "Casa", "address": nil},
		"quoted":   nil, "forwarded": false, "view_once": false,
	}
}

func TestAnonymizeStableRemovesPersonalData(t *testing.T) {
	anon := anonymizeStable(sampleStable(), "")
	raw, _ := json.Marshal(anon)
	out := string(raw)
	for _, secret := range []string{"5511999998888", "123456789", "Maria", "senha", "3EB0SECRETID", "deadbeef", "rg.jpg", "Ana", "90000", "Casa", "-23.5", "2048"} {
		if strings.Contains(out, secret) {
			t.Errorf("anonymized sample still contains %q: %s", secret, out)
		}
	}
	for _, kept := range []string{`"type":"image"`, `"kind":"image"`, `"mime":"image/jpeg"`, `"schema":1`, `"pn":"<jid>@s.whatsapp.net"`, `"lid":"<jid>@lid"`, `"url":"/message/<id>/media"`, `"text":"<text:10>"`, `"timestamp":"2000-01-01T00:00:00Z"`} {
		if !strings.Contains(out, kept) {
			t.Errorf("anonymized sample lost %s: %s", kept, out)
		}
	}
}

func TestWriteStableAuditCapsSamplesPerShape(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := writeStableAudit(dir, "message", sampleStable()); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "message", "image", "*.json"))
	if len(files) != stableAuditCap {
		t.Fatalf("samples for one shape = %d, want %d", len(files), stableAuditCap)
	}

	other := sampleStable()
	other["text"] = nil
	if err := writeStableAudit(dir, "message", other); err != nil {
		t.Fatal(err)
	}
	files, _ = filepath.Glob(filepath.Join(dir, "message", "image", "*.json"))
	if len(files) != stableAuditCap+1 {
		t.Fatalf("a new shape must get its own sample; files = %d", len(files))
	}

	raw, _ := os.ReadFile(files[0])
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc["event"] != "message" || doc["stable"] == nil {
		t.Fatalf("sample file = %s", raw)
	}
}

func TestAuditStableIsNoOpWithoutDir(t *testing.T) {
	t.Setenv("WHATSAPP_STABLE_AUDIT_DIR", "")
	auditStable("message", sampleStable())
}

func TestAuditStableReturnsImmediately(t *testing.T) {
	t.Setenv("WHATSAPP_STABLE_AUDIT_DIR", "/dev/null/not-a-dir")
	start := time.Now()
	auditStable("message", sampleStable())
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("auditStable must not wait for the disk")
	}
}
```

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'Anonymize|WriteStableAudit|AuditStable' -count=1`
Expected: FAIL to compile (`undefined: anonymizeStable`).

- [ ] **Step 2: Write `stable_audit.go`**

```go
package whatsapp

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sirupsen/logrus"
)

// Fork (elphant): production audit of payload.stable. With
// WHATSAPP_STABLE_AUDIT_DIR set, each new shape (event, type, non-null keys)
// is saved anonymized, up to stableAuditCap samples, for contract/cmd/compare.
// It never blocks or fails webhook delivery.

const stableAuditCap = 3

var stableAuditMu sync.Mutex

// Values kept verbatim: closed sets that carry no personal data.
var stableAuditKeep = map[string]bool{"schema": true, "type": true, "kind": true, "mime": true, "status": true, "emoji": true}

func auditStable(event string, stable any) {
	dir := os.Getenv("WHATSAPP_STABLE_AUDIT_DIR")
	if dir == "" {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logrus.Warnf("[STABLE_AUDIT] panic: %v", r)
			}
		}()
		if err := writeStableAudit(dir, event, stable); err != nil {
			logrus.Warnf("[STABLE_AUDIT] %s: %v", event, err)
		}
	}()
}

func writeStableAudit(dir, event string, stable any) error {
	raw, err := json.Marshal(stable)
	if err != nil {
		return err
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return err
	}
	anon, _ := anonymizeStable(tree, "").(map[string]any)
	typ, _ := tree["type"].(string)
	if typ == "" {
		typ = "-"
	}
	key := stableShapeKey(event, anon)
	sub := filepath.Join(dir, event, typ)

	stableAuditMu.Lock()
	defer stableAuditMu.Unlock()
	if err := os.MkdirAll(sub, 0o750); err != nil {
		return err
	}
	existing, _ := filepath.Glob(filepath.Join(sub, key+"-*.json"))
	if len(existing) >= stableAuditCap {
		return nil
	}
	out, err := json.MarshalIndent(map[string]any{"event": event, "stable": anon}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(sub, fmt.Sprintf("%s-%d.json", key, len(existing)+1)), append(out, '\n'), 0o640)
}

// anonymizeStable replaces personal values with type-preserving placeholders.
func anonymizeStable(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			out[k] = anonymizeStable(child, k)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, child := range x {
			out[i] = anonymizeStable(child, key)
		}
		return out
	case string:
		switch {
		case stableAuditKeep[key]:
			return x
		case key == "pn" || key == "lid":
			if i := strings.LastIndex(x, "@"); i >= 0 {
				return "<jid>" + x[i:]
			}
			return "<jid>"
		case key == "id" || key == "target_id" || key == "ids":
			return "<id>"
		case key == "url":
			return "/message/<id>/media"
		case key == "sha256":
			return "<sha256>"
		case key == "phones":
			return "<phone>"
		case key == "timestamp":
			return "2000-01-01T00:00:00Z"
		default:
			return fmt.Sprintf("<text:%d>", utf8.RuneCountInString(x))
		}
	case float64:
		if stableAuditKeep[key] {
			return x
		}
		return 0.0
	default:
		return x
	}
}

// stableShapeKey is a short file-name key of the shape: event, type and the
// sorted non-null paths.
func stableShapeKey(event string, tree map[string]any) string {
	var paths []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				p := strings.TrimPrefix(prefix+"."+k, ".")
				if child != nil {
					paths = append(paths, p)
				}
				walk(p, child)
			}
		case []any:
			for _, child := range x {
				walk(prefix+"[]", child)
			}
		}
	}
	walk("", tree)
	sort.Strings(paths)
	typ, _ := tree["type"].(string)
	sum := sha1.Sum([]byte(event + "|" + typ + "|" + strings.Join(paths, ",")))
	return hex.EncodeToString(sum[:])[:12]
}
```

- [ ] **Step 3: Remove the placeholder**

In `stable_payload.go`, delete:

```go
// auditStable is replaced by the real audit in stable_audit.go (Task 8).
func auditStable(string, any) {}
```

- [ ] **Step 4: Run the tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -count=1 && GOTOOLCHAIN=auto go test -race -count=1 ./infrastructure/whatsapp/ && GOTOOLCHAIN=auto go vet ./infrastructure/whatsapp/`
Expected: PASS; no race; vet clean.

- [ ] **Step 5: Commit**

```bash
git add src/infrastructure/whatsapp/stable_audit.go src/infrastructure/whatsapp/stable_audit_test.go src/infrastructure/whatsapp/stable_payload.go
git commit -m "feat(whatsapp): audit payload.stable shapes with anonymized samples"
```

---

### Task 9: Media streaming endpoint

**Files:**
- Create: `src/domains/message/media_stream.go`
- Modify: `src/domains/message/interfaces.go` (add `IMessageMediaStream` to `IMessageUsecase`)
- Create: `src/usecase/message_media_stream.go`
- Create: `src/ui/rest/message_media.go`
- Modify: `src/ui/rest/message.go` (one route line after `app.Get("/message/:message_id/download", rest.DownloadMedia)`)
- Test: `src/usecase/message_media_stream_test.go`

**Interfaces:**
- Produces: `domainMessage.ErrMediaNotFound`, `domainMessage.ErrMediaGone`; `type MediaStream struct { File io.ReadCloser; Size int64; Mime, Filename string }`; `IMessageMediaStream.StreamMedia(ctx context.Context, messageID string) (MediaStream, error)`; package var `mediaStreamDownloadFn func(ctx context.Context, client *whatsmeow.Client, msg whatsmeow.DownloadableMessage, file *os.File) error`; route `GET /message/:message_id/media`.

- [ ] **Step 1: Write the domain file and the failing test**

Create `src/domains/message/media_stream.go`:

```go
package message

import (
	"context"
	"errors"
	"io"
)

// Fork (elphant): GET /message/:message_id/media.

var (
	ErrMediaNotFound = errors.New("message has no downloadable media")
	ErrMediaGone     = errors.New("media is no longer available on WhatsApp")
)

// MediaStream is a downloaded media file ready to stream. Closing File
// removes it from the server.
type MediaStream struct {
	File     io.ReadCloser
	Size     int64
	Mime     string
	Filename string
}

type IMessageMediaStream interface {
	StreamMedia(ctx context.Context, messageID string) (MediaStream, error)
}
```

In `src/domains/message/interfaces.go`, inside `type IMessageUsecase interface {`, after `IMessageManagement` add:

```go
	IMessageMediaStream // Fork (elphant): see media_stream.go
```

Create `src/usecase/message_media_stream_test.go`:

```go
package usecase

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

type mediaStreamRepo struct {
	domainChatStorage.IChatStorageRepository
	msg *domainChatStorage.Message
}

func (r mediaStreamRepo) GetMessageByIDAndDevice(_, _ string) (*domainChatStorage.Message, error) {
	return r.msg, nil
}

func mediaStreamCtx() context.Context {
	jid := types.NewADJID("5511900000009", 0, 12)
	client := &whatsmeow.Client{Store: &store.Device{ID: &jid}}
	return whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("slot", client, nil))
}

func withDownload(t *testing.T, fn func(ctx context.Context, client *whatsmeow.Client, msg whatsmeow.DownloadableMessage, file *os.File) error) {
	t.Helper()
	previous := mediaStreamDownloadFn
	mediaStreamDownloadFn = fn
	t.Cleanup(func() { mediaStreamDownloadFn = previous })
}

var storedImage = &domainChatStorage.Message{
	ID: "IMG1", MediaType: "image", DirectPath: "/v/t62/abc", MediaKey: []byte{1}, FileSHA256: []byte{2}, FileEncSHA256: []byte{3}, FileLength: 4,
}

func TestStreamMediaNotFound(t *testing.T) {
	for name, msg := range map[string]*domainChatStorage.Message{"missing": nil, "no media": {ID: "TXT1"}} {
		t.Run(name, func(t *testing.T) {
			svc := serviceMessage{chatStorageRepo: mediaStreamRepo{msg: msg}}
			if _, err := svc.StreamMedia(mediaStreamCtx(), "X"); !errors.Is(err, domainMessage.ErrMediaNotFound) {
				t.Fatalf("err = %v, want ErrMediaNotFound", err)
			}
		})
	}
}

func TestStreamMediaGone(t *testing.T) {
	withDownload(t, func(context.Context, *whatsmeow.Client, whatsmeow.DownloadableMessage, *os.File) error {
		return whatsmeow.ErrMediaDownloadFailedWith410
	})
	svc := serviceMessage{chatStorageRepo: mediaStreamRepo{msg: storedImage}}
	if _, err := svc.StreamMedia(mediaStreamCtx(), "IMG1"); !errors.Is(err, domainMessage.ErrMediaGone) {
		t.Fatalf("err = %v, want ErrMediaGone", err)
	}
}

func TestStreamMediaRemovesTheTempDirOnDownloadError(t *testing.T) {
	var dir string
	withDownload(t, func(_ context.Context, _ *whatsmeow.Client, _ whatsmeow.DownloadableMessage, f *os.File) error {
		dir = dirOf(f.Name())
		return errors.New("socket closed")
	})
	svc := serviceMessage{chatStorageRepo: mediaStreamRepo{msg: storedImage}}
	if _, err := svc.StreamMedia(mediaStreamCtx(), "IMG1"); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %s still exists", dir)
	}
}

func TestStreamMediaStreamsAndCleansUp(t *testing.T) {
	var dir string
	withDownload(t, func(_ context.Context, _ *whatsmeow.Client, _ whatsmeow.DownloadableMessage, f *os.File) error {
		dir = dirOf(f.Name())
		_, err := f.Write([]byte("\x89PNG\r\n\x1a\nimage-bytes"))
		return err
	})
	svc := serviceMessage{chatStorageRepo: mediaStreamRepo{msg: storedImage}}
	stream, err := svc.StreamMedia(mediaStreamCtx(), "IMG1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(stream.File)
	if string(body) != "\x89PNG\r\n\x1a\nimage-bytes" || stream.Size != int64(len(body)) || stream.Mime != "image/png" {
		t.Fatalf("stream = %q size=%d mime=%s", body, stream.Size, stream.Mime)
	}
	if err := stream.File.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %s still exists after Close", dir)
	}
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == os.PathSeparator {
			return path[:i]
		}
	}
	return path
}
```

Run: `cd src && GOTOOLCHAIN=auto go test ./usecase/ -run StreamMedia -count=1`
Expected: FAIL to compile (`serviceMessage` does not implement `StreamMedia` / `undefined: mediaStreamDownloadFn`).

- [ ] **Step 2: Write `src/usecase/message_media_stream.go`**

```go
package usecase

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"go.mau.fi/whatsmeow"
)

// Fork (elphant): streams a message's media to the caller through a temp
// file that is deleted when the stream is closed; nothing lands in /statics.

var mediaStreamDownloadFn = func(ctx context.Context, client *whatsmeow.Client, msg whatsmeow.DownloadableMessage, file *os.File) error {
	return client.DownloadToFile(ctx, msg, file)
}

type tempMediaFile struct {
	*os.File
	dir string
}

func (f *tempMediaFile) Close() error {
	err := f.File.Close()
	_ = os.RemoveAll(f.dir)
	return err
}

func (service serviceMessage) StreamMedia(ctx context.Context, messageID string) (domainMessage.MediaStream, error) {
	client := whatsapp.ClientFromContext(ctx)
	if client == nil || client.Store == nil || client.Store.ID == nil {
		return domainMessage.MediaStream{}, pkgError.ErrWaCLI
	}
	stored, err := service.chatStorageRepo.GetMessageByIDAndDevice(client.Store.ID.ToNonAD().String(), messageID)
	if err != nil {
		return domainMessage.MediaStream{}, fmt.Errorf("look up message %s: %w", messageID, err)
	}
	if stored == nil || stored.MediaType == "" || utils.ResolveMediaDirectPath(stored.DirectPath, stored.URL) == "" {
		return domainMessage.MediaStream{}, domainMessage.ErrMediaNotFound
	}
	downloadable, err := utils.BuildDownloadableMessage(stored.MediaType, stored.URL, stored.DirectPath, stored.Filename,
		stored.MediaKey, stored.FileSHA256, stored.FileEncSHA256, stored.FileLength)
	if err != nil {
		return domainMessage.MediaStream{}, domainMessage.ErrMediaNotFound
	}

	dir, err := os.MkdirTemp("", "gowa-media-*")
	if err != nil {
		return domainMessage.MediaStream{}, err
	}
	file, err := os.Create(filepath.Join(dir, "media"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return domainMessage.MediaStream{}, err
	}
	fail := func(err error) (domainMessage.MediaStream, error) {
		_ = file.Close()
		_ = os.RemoveAll(dir)
		return domainMessage.MediaStream{}, err
	}
	if err := mediaStreamDownloadFn(ctx, client, downloadable, file); err != nil {
		if errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410) {
			return fail(domainMessage.ErrMediaGone)
		}
		return fail(fmt.Errorf("download media %s: %w", messageID, err))
	}
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	head := make([]byte, 512)
	n, _ := file.ReadAt(head, 0)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return domainMessage.MediaStream{
		File:     &tempMediaFile{File: file, dir: dir},
		Size:     info.Size(),
		Mime:     http.DetectContentType(head[:n]),
		Filename: stored.Filename,
	}, nil
}
```

- [ ] **Step 3: Write the handler and route**

Create `src/ui/rest/message_media.go`:

```go
package rest

import (
	"errors"
	"fmt"
	"net/http"

	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// Fork (elphant): GET /message/:message_id/media streams the media file and
// never writes it to /statics.
func (controller *Message) StreamMedia(c fiber.Ctx) error {
	stream, err := controller.Service.StreamMedia(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), c.Params("message_id"))
	switch {
	case errors.Is(err, domainMessage.ErrMediaNotFound):
		return c.Status(http.StatusNotFound).JSON(utils.ResponseData{Status: http.StatusNotFound, Code: "MEDIA_NOT_FOUND", Message: err.Error()})
	case errors.Is(err, domainMessage.ErrMediaGone):
		return c.Status(http.StatusGone).JSON(utils.ResponseData{Status: http.StatusGone, Code: "MEDIA_GONE", Message: err.Error()})
	case err != nil:
		utils.PanicIfNeeded(err)
	}
	c.Set(fiber.HeaderContentType, stream.Mime)
	if stream.Filename != "" {
		c.Set(fiber.HeaderContentDisposition, fmt.Sprintf("attachment; filename=%q", stream.Filename))
	}
	return c.SendStream(stream.File, int(stream.Size))
}
```

In `src/ui/rest/message.go`, after `app.Get("/message/:message_id/download", rest.DownloadMedia)` add:

```go
	app.Get("/message/:message_id/media", rest.StreamMedia) // Fork (elphant): see message_media.go
```

- [ ] **Step 4: Run tests, build, vet**

Run: `cd src && GOTOOLCHAIN=auto go test ./usecase/ ./ui/... -count=1 && GOTOOLCHAIN=auto go build ./... && GOTOOLCHAIN=auto go vet ./...`
Expected: PASS; build ok (test fakes that embed `IMessageUsecase` keep compiling); vet clean.

- [ ] **Step 5: Commit**

```bash
git add src/domains/message/media_stream.go src/domains/message/interfaces.go src/usecase/message_media_stream.go src/usecase/message_media_stream_test.go src/ui/rest/message_media.go src/ui/rest/message.go
git commit -m "feat(message): stream message media without writing to statics"
```

---

### Task 10: Documentation

**Files:**
- Modify: `docs/elphant-fork.md` (fork)
- Modify: `~/Documents/elphantcrm-whatsapp-gateway/docs/reference/38-gowa.md` (elphantcrm)

**Interfaces:** none.

- [ ] **Step 1: Add to `docs/elphant-fork.md`** a section after the `session.status` section:

````markdown
## `payload.stable`

Every message webhook event (`message`, `message.edited`, `message.reaction`,
`message.revoked`, `message.ack`) carries `payload.stable`, a closed object whose keys are always
present (`null` when unknown) and never change type. The existing payload fields are unchanged.
The contract is `contract/stable.schema.json`; examples are in `contract/fixtures/synthetic/`.

```json
"stable": {
  "schema": 1, "id": "3EB0…", "timestamp": "2026-09-25T12:00:00Z", "is_from_me": false,
  "chat": { "pn": "5511…@s.whatsapp.net", "lid": "123…@lid", "is_group": false },
  "sender": { "pn": "5511…@s.whatsapp.net", "lid": "123…@lid", "push_name": "Fulano" },
  "type": "image", "text": "caption", "media": { "kind": "image", "mime": "image/jpeg", "size": 2048,
  "sha256": "…", "filename": null, "duration": null, "ptt": false, "width": 640, "height": 480,
  "url": "/message/3EB0…/media" }, "location": null, "contact": null, "quoted": null,
  "forwarded": false, "view_once": false
}
```

Per event: `message` adds `type`, `text`, `media`, `location`, `contact`, `quoted`, `forwarded`,
`view_once`; `message.edited` adds `target_id`, `text`; `message.reaction` adds `target_id`,
`emoji` (`null` = removed); `message.revoked` adds `target_id`; `message.ack` adds `status`
(`delivered`, `read`, `played`) and `ids`.

## `GET /message/:message_id/media`

Streams the media of a stored message (Basic Auth, `X-Device-Id`). The file goes through a temp
file that is deleted after the response; nothing is written to `/statics`. `404` when the message
has no media, `410` when WhatsApp no longer has it: download soon after the webhook arrives.

## Stable payload audit

With `WHATSAPP_STABLE_AUDIT_DIR` set, each new shape of `payload.stable` (event, type and non-null
keys) is saved anonymized, at most 3 samples per shape. Reviewed samples go to
`contract/fixtures/real/`, and `cd contract && go run ./cmd/compare` reports real shapes without a
synthetic fixture.
````

- [ ] **Step 2: Add to `38-gowa.md`**
  - In the stack YAML `environment:` block add `WHATSAPP_STABLE_AUDIT_DIR: "/app/storages/stable-audit"`.
  - New section `## 8. Contrato \`payload.stable\` e auditoria` (Portuguese BR) with: what `stable` is (link to the fork's `docs/elphant-fork.md`); media via `GET /message/:message_id/media` with the 404/410 meanings and "baixar logo que o webhook chegar"; the audit cycle (samples in `gowa_storages/stable-audit/`, copy to a folder outside any repo with `scp -r root@<gateway-host>:/var/lib/docker/volumes/gowa_storages/_data/stable-audit ~/gowa-stable-audit`, owner review before commit, move approved files to `contract/fixtures/real/`, run `go run ./cmd/compare`, new synthetic case per `MISSING` line).

Run:
```bash
python3 -c 'import re; print(re.search(r"```yaml\n(.*?)```", open("/Users/eduardocarlos/Documents/elphantcrm-whatsapp-gateway/docs/reference/38-gowa.md").read(), re.S).group(1))' \
  | ssh root@<gateway-host> 'GOWA_VERSION=v0.0.0-check GOWA_BASIC_AUTH=user:check docker stack config -c - | grep -c WHATSAPP_STABLE_AUDIT_DIR'
```
Expected: `1` (the file renders and carries the variable).

- [ ] **Step 3: Commit both**

```bash
git add docs/elphant-fork.md
git commit -m "docs: document payload.stable, the media endpoint and the audit"
cd ~/Documents/elphantcrm-whatsapp-gateway && git add docs/reference/38-gowa.md && git commit -m "docs(reference): contrato payload.stable, mídia e auditoria no GOWA"
```

---

### Task 11 [GATED]: Release `v9.5.0-elphant.2`

- [ ] **Step 1: Full local gate**

Run: `cd src && GOTOOLCHAIN=auto go vet ./... && GOTOOLCHAIN=auto go test ./... -count=1 && GOTOOLCHAIN=auto go test -race -count=1 ./infrastructure/whatsapp/ ./pkg/stablepayload/ ./usecase/ && cd ../contract && GOTOOLCHAIN=auto go test ./... -count=1 && git -C .. diff --quiet HEAD -- src/go.mod src/go.sum && echo gomod-unchanged`
Expected: all PASS; `gomod-unchanged`.

- [ ] **Step 2: Ask the owner for OK** to push `elphant`, tag `v9.5.0-elphant.2`, and update the `gowa` stack (image + `WHATSAPP_STABLE_AUDIT_DIR`), which restarts the container once.

- [ ] **Step 3: Push, CI, tag, image**

```bash
git push origin elphant
gh run watch "$(gh run list --repo eduardoelphant/go-whatsapp-web-multidevice --workflow 'Elphant CI' --branch elphant --limit 1 --json databaseId --jq '.[0].databaseId')" --repo eduardoelphant/go-whatsapp-web-multidevice --exit-status
git tag -a v9.5.0-elphant.2 -m v9.5.0-elphant.2 && git push origin v9.5.0-elphant.2
```
Then watch `Elphant image`. Expected: CI green (including `Contract tests`); image run green.

- [ ] **Step 4: Update the stack** through the Portainer API (`PUT /api/stacks/12?endpointId=1`, body built from the doc 38 YAML with `GOWA_VERSION=v9.5.0-elphant.2` and the current `GOWA_BASIC_AUTH`, sent with `curl`, temp body file deleted).
Expected: update `completed`, device `logged_in`, 0 decrypt/logout errors in the logs.

---

### Task 12 [GATED]: Production verification

- [ ] **Step 1: Ask the owner** to send, from another phone to the test number, one message of each: text, image with caption, video, voice note, audio file, document, sticker, location, contact, poll, reply, forwarded, edit, reaction, reaction removal, and delete-for-everyone; and one of each from the test number's own phone.

- [ ] **Step 2: Collect the samples outside any repo**

Run: `rm -rf ~/gowa-stable-audit && scp -rq root@<gateway-host>:/var/lib/docker/volumes/gowa_storages/_data/stable-audit ~/gowa-stable-audit && find ~/gowa-stable-audit -name '*.json' | wc -l`
Expected: a positive count, grouped by `<event>/<type>/`.

- [ ] **Step 3: Validate and compare before any commit**

Run: `rm -rf /tmp/g4-check && mkdir -p /tmp/g4-check/real && cp -R contract/fixtures/synthetic /tmp/g4-check/ && find ~/gowa-stable-audit -name '*.json' -exec cp {} /tmp/g4-check/real/ \; && cd contract && GOTOOLCHAIN=auto go run ./cmd/compare -dir /tmp/g4-check`
Expected: the `synthetic=… real=…` summary; every `MISSING`/`KIND` line becomes a new synthetic case in `golden_test.go` (Task 4 pattern: add the case, `-update`, inspect, commit `test(stablepayload): cover <shape> seen in production`).

- [ ] **Step 4: Media endpoint check**

whatsmeow's `DownloadToFile` already rejects a file whose SHA-256 differs from the one WhatsApp
sent, so a `200` proves integrity; the check below also compares with the stored hash. Pick the
id of the owner's test image (from the gateway log line of that message) and run:

```bash
A="$(cat ~/.config/elphant/gowa-basic-auth)"; D=<device id from GET /devices>; ID=<message id>
curl -s -u "$A" -H "X-Device-Id: $D" -o /tmp/g4-media -w '%{http_code} %{content_type}\n' "https://devias.elphant.com.br/message/$ID/media"
shasum -a 256 /tmp/g4-media | cut -c1-64; rm -f /tmp/g4-media
ssh root@<gateway-host> "python3 -c \"import sqlite3; c=sqlite3.connect('file:/var/lib/docker/volumes/gowa_storages/_data/chatstorage.db?mode=ro', uri=True); print(c.execute('select hex(file_sha256) from messages where id=?', ('$ID',)).fetchone()[0].lower())\""
curl -s -u "$A" -H "X-Device-Id: $D" -o /dev/null -w '%{http_code}\n' "https://devias.elphant.com.br/message/<id of a text message>/media"
```
Expected: `200 image/jpeg`; the two hashes are equal; the text message returns `404`.

- [ ] **Step 5: Owner review of real samples**, then (with OK) copy the approved files to `contract/fixtures/real/`, run `cd contract && go test ./... && go run ./cmd/compare`, commit `test(contract): add reviewed real fixtures`, push.
