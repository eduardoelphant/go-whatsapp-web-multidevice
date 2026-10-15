package call

import "context"

type ICallUsecase interface {
	RejectCall(ctx context.Context, callerJID string, callID string) error
	// Fork (elphant): voice calls.
	AnswerCall(ctx context.Context, request AnswerCallRequest) (AnswerCallResponse, error)
	StartCall(ctx context.Context, request StartCallRequest) (StartCallResponse, error)
	HangupCall(ctx context.Context, callID string) error
	ActiveCalls(ctx context.Context) ([]CallInfo, error)
	GetCall(ctx context.Context, callID string) (CallDetail, error)
	SetDeviceCalls(ctx context.Context, deviceID string, enabled bool) (DeviceCallsResponse, error)
	GetDeviceCalls(ctx context.Context, deviceID string) (DeviceCallsResponse, error)
}

type RejectCallRequest struct {
	CallerJID string `json:"caller_jid" form:"caller_jid"`
	CallID    string `json:"call_id" form:"call_id"`
}

// Fork (elphant): voice calls.

type CallIDRequest struct {
	CallID string `json:"call_id" form:"call_id"`
}

// AnswerCallRequest carries the browser's SDP answer to the call's offer_sdp.
type AnswerCallRequest struct {
	CallID string `json:"call_id" form:"call_id"`
	SDP    string `json:"sdp" form:"sdp"`
}

type AnswerCallResponse struct {
	CallID string `json:"call_id"`
}

// StartCallRequest carries the browser's SDP offer.
type StartCallRequest struct {
	Phone string `json:"phone" form:"phone"`
	SDP   string `json:"sdp" form:"sdp"`
}

type StartCallResponse struct {
	CallID    string `json:"call_id"`
	SDPAnswer string `json:"sdp_answer"`
}

type CallDetail struct {
	CallID    string   `json:"call_id"`
	State     string   `json:"state"`
	Direction string   `json:"direction"`
	Peer      CallPeer `json:"peer"`
	OfferSDP  string   `json:"offer_sdp,omitempty"`
}

type CallPeer struct {
	PN  string `json:"pn"`
	LID string `json:"lid"`
}

type CallInfo struct {
	CallID    string   `json:"call_id"`
	Direction string   `json:"direction"`
	Peer      CallPeer `json:"peer"`
	State     string   `json:"state"`
	StartedAt string   `json:"started_at"`
}

type DeviceCallsRequest struct {
	Enabled *bool `json:"enabled" form:"enabled"`
}

type DeviceCallsResponse struct {
	Enabled bool `json:"enabled"`
}
