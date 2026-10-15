package usecase

import (
	"context"
	"errors"
	"fmt"
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
		CallID: c.CallID, Direction: c.Direction, Peer: c.Peer, State: c.State,
		StartedAt: c.StartedAt.UTC().Format(time.RFC3339),
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

func (service serviceCall) AnswerCall(ctx context.Context, callID string) error {
	if callID == "" {
		return pkgError.ValidationError("call_id is required")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return err
	}
	return mapCallError(m.Answer(callID), "call operation failed: ")
}

func (service serviceCall) StartCall(ctx context.Context, phone string) (domainCall.CallInfo, error) {
	if phone == "" {
		return domainCall.CallInfo{}, pkgError.ValidationError("phone is required")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return domainCall.CallInfo{}, err
	}
	// meowcaller uses this ctx only for signaling (peer resolution, device discovery,
	// offer send in engine.go placeCall); the Call does not keep it, so a timeout is safe.
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := m.Start(startCtx, phone)
	if err != nil {
		return domainCall.CallInfo{}, mapCallError(err, "could not place the call: ")
	}
	return toCallInfo(info), nil
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
