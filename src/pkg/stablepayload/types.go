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
