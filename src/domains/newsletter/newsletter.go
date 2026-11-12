package newsletter

import "context"

type INewsletterUsecase interface {
	Unfollow(ctx context.Context, request UnfollowRequest) (err error)
	GetMessages(ctx context.Context, request GetMessagesRequest) (response GetMessagesResponse, err error)
	DownloadMedia(ctx context.Context, request DownloadMediaRequest) (response DownloadMediaResponse, err error)
	// Fork (elphant): channel data and creation.
	Info(ctx context.Context, request InfoRequest) (response NewsletterInfoResponse, err error)
	Create(ctx context.Context, request CreateRequest) (response NewsletterInfoResponse, err error)
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
