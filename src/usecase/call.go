package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domainCall "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/call"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/sirupsen/logrus"
)

type serviceCall struct{}

func NewCallService() domainCall.ICallUsecase {
	return &serviceCall{}
}

func (service serviceCall) RejectCall(ctx context.Context, callerJID string, callID string) error {
	if err := validations.ValidateRejectCall(ctx, callerJID, callID); err != nil {
		return err
	}
	if handled, err := rejectTrackedCall(ctx, callID); handled { // Fork (elphant): a call GOWA tracks is rejected through its manager (call.ended REJECTED).
		return err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return pkgError.ErrWaCLI
	}

	utils.MustLogin(client)
	parsedJID, err := utils.ParseJID(callerJID)
	if err != nil {
		return err
	}

	rejectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := client.RejectCall(rejectCtx, parsedJID, callID); err != nil {
		logrus.WithError(err).Error("Failed to reject call")
		return fmt.Errorf("failed to reject call: %w", err)
	}

	logrus.Info("Rejected call successfully")
	return nil
}

// Fork (elphant): voice calls.

func toCallInfo(c whatsapp.CallInfo) domainCall.CallInfo {
	return domainCall.CallInfo{
		CallID: c.CallID, Direction: c.Direction, Peer: domainCall.CallPeer{PN: c.Peer.PN, LID: c.Peer.LID},
		State: c.State, StartedAt: c.StartedAt.UTC().Format(time.RFC3339),
	}
}

// mapCallError turns engine errors, which can embed full phone numbers, into a masked
// ValidationError. Errors that already carry an HTTP status pass through.
func mapCallError(err error, prefix string) error {
	if err == nil {
		return nil
	}
	var ge pkgError.GenericError
	if errors.As(err, &ge) {
		return err
	}
	return pkgError.ValidationError(prefix + whatsapp.MaskDigits(err.Error()))
}

// rejectTrackedCall rejects a call the device's call manager tracks; handled is false when
// calls are off for the device or the manager does not know the call (whatsmeow rejects it).
func rejectTrackedCall(ctx context.Context, callID string) (handled bool, err error) {
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return false, nil
	}
	if _, ok := m.Get(callID); !ok {
		return false, nil
	}
	return true, mapCallError(m.Reject(callID), "call operation failed: ")
}

func (service serviceCall) AnswerCall(ctx context.Context, request domainCall.AnswerCallRequest) (domainCall.AnswerCallResponse, error) {
	if request.CallID == "" {
		return domainCall.AnswerCallResponse{}, pkgError.ValidationError("call_id is required")
	}
	if strings.TrimSpace(request.SDP) == "" {
		return domainCall.AnswerCallResponse{}, pkgError.ValidationError("sdp is required (the browser's answer to offer_sdp)")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return domainCall.AnswerCallResponse{}, err
	}
	if err := m.Answer(request.CallID, request.SDP); err != nil {
		return domainCall.AnswerCallResponse{}, mapCallError(err, "call operation failed: ")
	}
	return domainCall.AnswerCallResponse{CallID: request.CallID}, nil
}

func (service serviceCall) StartCall(ctx context.Context, request domainCall.StartCallRequest) (domainCall.StartCallResponse, error) {
	if request.Phone == "" {
		return domainCall.StartCallResponse{}, pkgError.ValidationError("phone is required")
	}
	if strings.TrimSpace(request.SDP) == "" {
		return domainCall.StartCallResponse{}, pkgError.ValidationError("sdp is required (the browser's offer)")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return domainCall.StartCallResponse{}, err
	}
	// meowcaller uses this ctx only for signaling (peer resolution, device discovery,
	// offer send in engine.go placeCall); the Call does not keep it, so a timeout is safe.
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, answer, err := m.Start(startCtx, request.Phone, request.SDP)
	if err != nil {
		return domainCall.StartCallResponse{}, mapCallError(err, "could not place the call: ")
	}
	return domainCall.StartCallResponse{CallID: info.CallID, SDPAnswer: answer}, nil
}

func (service serviceCall) GetCall(ctx context.Context, callID string) (domainCall.CallDetail, error) {
	if callID == "" {
		return domainCall.CallDetail{}, pkgError.ValidationError("call_id is required")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return domainCall.CallDetail{}, err
	}
	info, offer, err := m.Detail(ctx, callID)
	if err != nil {
		return domainCall.CallDetail{}, mapCallError(err, "call operation failed: ")
	}
	return domainCall.CallDetail{
		CallID: info.CallID, State: info.State, Direction: info.Direction,
		Peer: domainCall.CallPeer{PN: info.Peer.PN, LID: info.Peer.LID}, OfferSDP: offer,
	}, nil
}

func (service serviceCall) HangupCall(ctx context.Context, callID string) error {
	if callID == "" {
		return pkgError.ValidationError("call_id is required")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return err
	}
	return mapCallError(m.Hangup(callID), "call operation failed: ")
}

func (service serviceCall) ActiveCalls(ctx context.Context) ([]domainCall.CallInfo, error) {
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return nil, err
	}
	calls := m.Active()
	out := make([]domainCall.CallInfo, 0, len(calls))
	for _, c := range calls {
		out = append(out, toCallInfo(c))
	}
	return out, nil
}

func (service serviceCall) SetDeviceCalls(_ context.Context, deviceID string, enabled bool) (domainCall.DeviceCallsResponse, error) {
	on, err := whatsapp.SetDeviceCalls(deviceID, enabled)
	if err != nil {
		return domainCall.DeviceCallsResponse{}, err
	}
	return domainCall.DeviceCallsResponse{Enabled: on}, nil
}

func (service serviceCall) GetDeviceCalls(_ context.Context, deviceID string) (domainCall.DeviceCallsResponse, error) {
	on, err := whatsapp.DeviceCalls(deviceID)
	if err != nil {
		return domainCall.DeviceCallsResponse{}, err
	}
	return domainCall.DeviceCallsResponse{Enabled: on}, nil
}
