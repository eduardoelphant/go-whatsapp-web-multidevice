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
