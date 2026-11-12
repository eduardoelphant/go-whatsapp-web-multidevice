package error

import "net/http"

type LoginError string

// Error for complying the error interface
func (e LoginError) Error() string {
	return string(e)
}

// ErrCode will return the error code based on the error data type
func (e LoginError) ErrCode() string {
	return "ALREADY_LOGGED_IN"
}

// StatusCode will return the HTTP status code based on the error data type
func (e LoginError) StatusCode() int {
	return http.StatusBadRequest
}

type AuthError string

func (err AuthError) Error() string {
	return string(err)
}

// ErrCode will return the error code based on the error data type
func (err AuthError) ErrCode() string {
	return "AUTHENTICATION_ERROR"
}

// StatusCode will return the HTTP status code based on the error data type
func (err AuthError) StatusCode() int {
	return http.StatusUnauthorized
}

type qrChannelError string

func (err qrChannelError) Error() string {
	return string(err)
}

// ErrCode will return the error code based on the error data type
func (err qrChannelError) ErrCode() string {
	return "QR_CHANNEL_ERROR"
}

// StatusCode will return the HTTP status code based on the error data type
func (err qrChannelError) StatusCode() int {
	return http.StatusInternalServerError
}

type sessionSavedError string

func (err sessionSavedError) Error() string {
	return string(err)
}

// ErrCode will return the error code based on the error data type
func (err sessionSavedError) ErrCode() string {
	return "SESSION_SAVED_ERROR"
}

// StatusCode will return the HTTP status code based on the error data type
func (err sessionSavedError) StatusCode() int {
	return http.StatusInternalServerError
}

type notFoundError string

func (err notFoundError) Error() string {
	return string(err)
}

// ErrCode will return the error code based on the error data type
func (err notFoundError) ErrCode() string {
	return "NOT_FOUND"
}

// StatusCode will return the HTTP status code based on the error data type
func (err notFoundError) StatusCode() int {
	return http.StatusNotFound
}

// forbiddenError answers 403 when WhatsApp refuses an action for lack of role.
type forbiddenError string

func (err forbiddenError) Error() string { return string(err) }

// ErrCode will return the error code based on the error data type
func (err forbiddenError) ErrCode() string { return "NOT_AUTHORIZED" }

// StatusCode will return the HTTP status code based on the error data type
func (err forbiddenError) StatusCode() int { return http.StatusForbidden }

// mediaRejectedError answers 400 when WhatsApp refuses the uploaded media.
type mediaRejectedError string

func (err mediaRejectedError) Error() string { return string(err) }

// ErrCode will return the error code based on the error data type
func (err mediaRejectedError) ErrCode() string { return "MEDIA_REJECTED" }

// StatusCode will return the HTTP status code based on the error data type
func (err mediaRejectedError) StatusCode() int { return http.StatusBadRequest }

// MediaRejected builds the 400 MEDIA_REJECTED error carrying the server text.
func MediaRejected(text string) error { return mediaRejectedError(text) }

// editRefusedError answers 422 when WhatsApp refuses to edit a channel post.
type editRefusedError string

func (err editRefusedError) Error() string { return string(err) }

// ErrCode will return the error code based on the error data type
func (err editRefusedError) ErrCode() string { return "EDIT_REFUSED" }

// StatusCode will return the HTTP status code based on the error data type
func (err editRefusedError) StatusCode() int { return http.StatusUnprocessableEntity }

// EditRefused builds the 422 EDIT_REFUSED error carrying the server text.
func EditRefused(text string) error { return editRefusedError(text) }

// NotAuthorized builds the 403 NOT_AUTHORIZED error carrying the server text.
func NotAuthorized(text string) error { return forbiddenError(text) }

var (
	ErrAlreadyLoggedIn       = LoginError("you are already logged in.")
	ErrNotConnected          = AuthError("you are not connect to services server, please reconnect")
	ErrNotLoggedIn           = AuthError("you are not logged in")
	ErrReconnect             = AuthError("reconnect error")
	ErrQrChannel             = qrChannelError("QR channel error")
	ErrSessionSaved          = sessionSavedError("your session have been saved, please wait to connect 2 second and refresh again")
	ErrDeviceNotFound        = notFoundError("device not found")
	ErrScheduledSendNotFound = notFoundError("scheduled send not found")
	// Fork (elphant): avatar lookups that find no picture answer 404 instead of a 500.
	ErrAvatarHidden = notFoundError("the user has hidden their profile picture from you")
	// Fork (elphant): voting on a poll the device does not know answers 404.
	ErrPollNotFound = notFoundError("poll not found for this device and chat")
	ErrAvatarNotSet = notFoundError("that user or group does not have a profile picture")
	// Fork (elphant): newsletter (channel) lookups that find nothing answer 404.
	ErrNewsletterNotFound = notFoundError("that newsletter does not exist")
)
