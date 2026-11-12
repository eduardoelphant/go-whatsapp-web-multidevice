package newsletter

import "context"

type INewsletterUsecase interface {
	Unfollow(ctx context.Context, request UnfollowRequest) (err error)
	GetMessages(ctx context.Context, request GetMessagesRequest) (response GetMessagesResponse, err error)
	DownloadMedia(ctx context.Context, request DownloadMediaRequest) (response DownloadMediaResponse, err error)
	// Fork (elphant): channel data and creation.
	Info(ctx context.Context, request InfoRequest) (response NewsletterInfoResponse, err error)
	Create(ctx context.Context, request CreateRequest) (response NewsletterInfoResponse, err error)
	// Fork (elphant): publish one post to a channel.
	Send(ctx context.Context, request SendRequest) (response SendResponse, err error)
	// Fork (elphant): edit the text of a text post, and delete a post for everyone.
	Edit(ctx context.Context, request EditRequest) (response EditResponse, err error)
	Revoke(ctx context.Context, request RevokeRequest) (response EditResponse, err error)
	// Fork (elphant): edit name, description and picture of the channel.
	Update(ctx context.Context, request UpdateRequest) (response NewsletterInfoResponse, err error)
}

type UnfollowRequest struct {
	NewsletterID string `json:"newsletter_id" form:"newsletter_id"`
}

// GetMessagesRequest fetches the latest messages posted in a newsletter (channel)
// directly from WhatsApp servers.
type GetMessagesRequest struct {
	NewsletterID string `json:"newsletter_id" query:"newsletter_id"`
	// Count limits how many messages are returned. Defaults to 50, capped at 100.
	Count int `json:"count" query:"count"`
	// Before paginates backwards from a given message server ID (exclusive),
	// used to load older messages than the ones already fetched.
	Before int `json:"before" query:"before"`
}

// Message represents a single newsletter (channel) message returned by
// GetMessagesRequest.
type Message struct {
	ServerID       int            `json:"server_id"`
	MessageID      string         `json:"message_id"`
	Type           string         `json:"type"`
	Timestamp      string         `json:"timestamp"`
	ViewsCount     int            `json:"views_count"`
	ReactionCounts map[string]int `json:"reaction_counts,omitempty"`
	Text           string         `json:"text,omitempty"`
	// Fork (elphant): forwards, kind of post and caption of media posts.
	ForwardsCount int    `json:"forwards_count"`
	MediaType     string `json:"media_type,omitempty"`
	Caption       string `json:"caption,omitempty"`
}

type GetMessagesResponse struct {
	Data []Message `json:"data"`
}

type DownloadMediaRequest struct {
	NewsletterID string `json:"newsletter_id" query:"newsletter_id"`
	ServerID     int    `json:"server_id" uri:"server_id"`
}

type DownloadMediaResponse struct {
	ServerID  int    `json:"server_id"`
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
	MediaType string `json:"media_type"`
	MimeType  string `json:"mime_type"`
	Filename  string `json:"filename"`
	FilePath  string `json:"file_path"`
	FileURL   string `json:"file_url,omitempty"`
	FileSize  int64  `json:"file_size"`
}

// InfoRequest reads one channel the device is joined to.
type InfoRequest struct {
	NewsletterID string `json:"newsletter_id" query:"newsletter_id"`
}

// CreateRequest creates a channel. Picture carries the raw JPEG bytes, sent as is.
type CreateRequest struct {
	Name        string `json:"name" form:"name"`
	Description string `json:"description" form:"description"`
	Picture     []byte `json:"-" form:"-"`
}

// NewsletterInfoResponse is the channel shape shared by every newsletter route
// that returns channel data.
type NewsletterInfoResponse struct {
	NewsletterID     string `json:"newsletter_id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Invite           string `json:"invite"`
	SubscribersCount int    `json:"subscribers_count"`
	State            string `json:"state"`
	Role             string `json:"role"`
	Verification     string `json:"verification"`
	PictureURL       string `json:"picture_url"`
}

// Post types accepted by POST /newsletter/send.
const (
	SendTypeText     = "text"
	SendTypeImage    = "image"
	SendTypeVideo    = "video"
	SendTypeAudio    = "audio"
	SendTypeDocument = "document"
	SendTypeSticker  = "sticker"
	SendTypePoll     = "poll"
)

// SendRequest publishes one post to a channel. File carries the raw bytes of the
// multipart "file" field; MessageID, when set, becomes the WhatsApp message id so the
// caller can find the post again after an uncertain answer.
type SendRequest struct {
	NewsletterID  string   `json:"newsletter_id" form:"newsletter_id"`
	MessageID     string   `json:"message_id" form:"message_id"`
	Type          string   `json:"type" form:"type"`
	Text          string   `json:"text" form:"text"`
	LinkPreview   bool     `json:"link_preview" form:"link_preview"`
	Filename      string   `json:"filename" form:"filename"`
	PollQuestion  string   `json:"poll_question" form:"poll_question"`
	PollOptions   []string `json:"poll_options" form:"-"`
	PollMaxAnswer int      `json:"poll_max_answer" form:"poll_max_answer"`
	File          []byte   `json:"-" form:"-"`
	FileName      string   `json:"-" form:"-"`
}

// SendResponse is what WhatsApp confirmed: the message id, the server id of the post
// in the channel and the server timestamp (RFC 3339).
type SendResponse struct {
	MessageID string `json:"message_id"`
	ServerID  int    `json:"server_id"`
	Timestamp string `json:"timestamp"`
}

// EditRequest replaces the text of a published text post. MessageID comes from
// the route.
type EditRequest struct {
	NewsletterID string `json:"newsletter_id" form:"newsletter_id"`
	MessageID    string `json:"message_id" uri:"message_id"`
	Text         string `json:"text" form:"text"`
}

// RevokeRequest deletes a published post for everyone. MessageID comes from the route.
type RevokeRequest struct {
	NewsletterID string `json:"newsletter_id" form:"newsletter_id"`
	MessageID    string `json:"message_id" uri:"message_id"`
}

// EditResponse is the answer of edit and revoke: the message id of the post.
type EditResponse struct {
	MessageID string `json:"message_id"`
}

// UpdateRequest edits the channel itself. A nil Name or Description is not sent; an empty
// Description clears it. Picture carries raw JPEG bytes; RemovePicture clears the picture.
type UpdateRequest struct {
	NewsletterID  string  `json:"newsletter_id" form:"newsletter_id"`
	Name          *string `json:"name" form:"-"`
	Description   *string `json:"description" form:"-"`
	Picture       []byte  `json:"-" form:"-"`
	RemovePicture bool    `json:"remove_picture" form:"remove_picture"`
}
