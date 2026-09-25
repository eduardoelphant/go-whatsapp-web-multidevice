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

	// TypeInteractive is a message with buttons, a template, a list or a
	// native flow; TypeInteractiveReply is the choice someone made on one.
	TypeInteractive      = "interactive"
	TypeInteractiveReply = "interactive_reply"

	TypePollVote = "poll_vote"
	TypeCall     = "call"
	TypeProduct  = "product"
	TypeOrder    = "order"
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
	Type        string       `json:"type"`
	Text        *string      `json:"text"`
	Media       *Media       `json:"media"`
	Location    *Location    `json:"location"`
	Contact     *Contact     `json:"contact"`
	Quoted      *Quoted      `json:"quoted"`
	Forwarded   bool         `json:"forwarded"`
	ViewOnce    bool         `json:"view_once"`
	Interactive *Interactive `json:"interactive"`
	Reply       *Reply       `json:"reply"`
	Referral    *Referral    `json:"referral"`
	Poll        *Poll        `json:"poll"`
	PollVote    *PollVote    `json:"poll_vote"`
	Call        *Call        `json:"call"`
	Product     *Product     `json:"product"`
	Order       *Order       `json:"order"`
}

// Poll lists the options of a poll creation (type "poll").
type Poll struct {
	Options         []string `json:"options"`
	SelectableCount *int     `json:"selectable_count"`
}

// PollVote is a vote on a poll. Votes are encrypted; GOWA decrypts them when
// it knows the poll, and resolution says how far that went.
type PollVote struct {
	PollID     string   `json:"poll_id"`
	Selected   []string `json:"selected"`
	Resolution string   `json:"resolution"`
}

// Call is the call log entry WhatsApp adds to the chat after a call.
type Call struct {
	Outcome  *string `json:"outcome"`
	Video    bool    `json:"video"`
	Duration *int    `json:"duration"`
	CallType *string `json:"call_type"`
}

// Product is a catalog product shared in the chat. Amounts are in
// thousandths of the currency unit, as WhatsApp sends them (R$ 12,99 = 12990).
type Product struct {
	ID            *string `json:"id"`
	Title         *string `json:"title"`
	Description   *string `json:"description"`
	RetailerID    *string `json:"retailer_id"`
	URL           *string `json:"url"`
	Currency      *string `json:"currency"`
	Price1000     *int64  `json:"price_1000"`
	SalePrice1000 *int64  `json:"sale_price_1000"`
}

// Order is a catalog order; amounts in thousandths of the currency unit.
type Order struct {
	ID        *string `json:"id"`
	Title     *string `json:"title"`
	ItemCount *int    `json:"item_count"`
	Status    *string `json:"status"`
	Currency  *string `json:"currency"`
	Total1000 *int64  `json:"total_1000"`
}

// Interactive describes a message with buttons, a template, a list or a
// native flow. Buttons and Sections are never null.
type Interactive struct {
	Kind     string    `json:"kind"`
	Header   *string   `json:"header"`
	Footer   *string   `json:"footer"`
	Buttons  []Button  `json:"buttons"`
	Sections []Section `json:"sections"`
}

type Button struct {
	Kind  string  `json:"kind"`
	ID    *string `json:"id"`
	Text  *string `json:"text"`
	Value *string `json:"value"`
}

type Section struct {
	Title *string `json:"title"`
	Rows  []Row   `json:"rows"`
}

type Row struct {
	ID          *string `json:"id"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

// Reply is the option chosen on an interactive message; its text is in
// Message.Text.
type Reply struct {
	Kind string  `json:"kind"`
	ID   *string `json:"id"`
}

// Referral carries Click-to-WhatsApp ad attribution and the conversation
// entry point (wa.me links with source/medium).
type Referral struct {
	SourceType   *string    `json:"source_type"`
	SourceApp    *string    `json:"source_app"`
	SourceID     *string    `json:"source_id"`
	SourceURL    *string    `json:"source_url"`
	CtwaClid     *string    `json:"ctwa_clid"`
	Ref          *string    `json:"ref"`
	Title        *string    `json:"title"`
	Body         *string    `json:"body"`
	MediaType    *string    `json:"media_type"`
	ThumbnailURL *string    `json:"thumbnail_url"`
	MediaURL     *string    `json:"media_url"`
	EntryPoint   EntryPoint `json:"entry_point"`
}

type EntryPoint struct {
	Source         *string `json:"source"`
	App            *string `json:"app"`
	ExternalSource *string `json:"external_source"`
	ExternalMedium *string `json:"external_medium"`
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
