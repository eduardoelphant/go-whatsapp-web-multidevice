package usecase

import (
	"context"
	"errors"
	"net/http"

	domainLabel "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/label"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
)

// labelSyncConflict é o erro 409 de sincronização já em curso.
type labelSyncConflict string

func (e labelSyncConflict) Error() string   { return string(e) }
func (e labelSyncConflict) ErrCode() string { return "LABEL_SYNC_RUNNING" }
func (e labelSyncConflict) StatusCode() int { return http.StatusConflict }

type serviceLabel struct{}

func NewLabelService() domainLabel.ILabelUsecase {
	return &serviceLabel{}
}

func (serviceLabel) SyncLabels(ctx context.Context) (response domainLabel.SyncLabelsResponse, err error) {
	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return response, pkgError.ErrWaCLI
	}
	if !client.IsConnected() || !client.IsLoggedIn() {
		return response, pkgError.ErrNotConnected
	}

	deviceID := ""
	if client.Store != nil && client.Store.ID != nil {
		deviceID = client.Store.ID.ToNonAD().String()
	}
	if inst, ok := whatsapp.DeviceFromContext(ctx); ok && inst != nil && inst.JID() != "" {
		deviceID = inst.JID()
	}

	syncID, err := whatsapp.StartLabelSync(client, deviceID)
	if errors.Is(err, whatsapp.ErrLabelSyncRunning) {
		return response, labelSyncConflict(err.Error())
	}
	if err != nil {
		return response, err
	}
	response.SyncID = syncID

	return response, nil
}
