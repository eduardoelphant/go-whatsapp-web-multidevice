package call

import "context"

type ICallUsecase interface {
	RejectCall(ctx context.Context, callerJID string, callID string) error
	// Fork (elphant): voice calls.
	AnswerCall(ctx context.Context, callID string) error
	StartCall(ctx context.Context, phone string) (CallInfo, error)
	HangupCall(ctx context.Context, callID string) error
	ActiveCalls(ctx context.Context) ([]CallInfo, error)
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

type StartCallRequest struct {
	Phone string `json:"phone" form:"phone"`
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
