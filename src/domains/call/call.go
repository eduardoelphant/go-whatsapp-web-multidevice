package call

import "context"

type ICallUsecase interface {
	RejectCall(ctx context.Context, callerJID string, callID string) error
	// Fork (elphant): voice calls.
	AnswerCall(ctx context.Context, callID string) error
	StartCall(ctx context.Context, phone string) (CallInfo, error)
	HangupCall(ctx context.Context, callID string) error
	ActiveCalls(ctx context.Context) ([]CallInfo, error)
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

type CallInfo struct {
	CallID    string `json:"call_id"`
	Direction string `json:"direction"`
	Peer      string `json:"peer"`
	State     string `json:"state"`
	StartedAt string `json:"started_at"`
}
