package usecase

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	domainLabel "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/label"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
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

// labelClient devolve o cliente do device da requisição, já conectado e logado.
func labelClient(ctx context.Context) (*whatsmeow.Client, string, error) {
	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return nil, "", pkgError.ErrWaCLI
	}
	if !client.IsConnected() || !client.IsLoggedIn() {
		return nil, "", pkgError.ErrNotConnected
	}

	deviceID := ""
	if client.Store != nil && client.Store.ID != nil {
		deviceID = client.Store.ID.ToNonAD().String()
	}
	if inst, ok := whatsapp.DeviceFromContext(ctx); ok && inst != nil && inst.JID() != "" {
		deviceID = inst.JID()
	}

	return client, deviceID, nil
}

func (serviceLabel) SyncLabels(ctx context.Context) (response domainLabel.SyncLabelsResponse, err error) {
	client, deviceID, err := labelClient(ctx)
	if err != nil {
		return response, err
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

// nextLabelID escolhe o id da etiqueta nova: maior id conhecido + 1 (D7454). Sem id conhecido,
// roda a sincronização completa antes; se ela falhar, devolve 502.
func nextLabelID(known func() (int64, bool), syncNow func() error) (string, error) {
	max, ok := known()
	if !ok {
		if err := syncNow(); err != nil {
			if errors.Is(err, whatsapp.ErrLabelSyncRunning) {
				return "", labelSyncConflict(err.Error())
			}

			return "", pkgError.Upstream("could not learn existing label ids: " + err.Error())
		}
		// Conta vazia (nenhuma etiqueta) também é um estado válido: começa em 1.
		max, _ = known()
	}

	return strconv.FormatInt(max+1, 10), nil
}

func buildLabelUpsertPatch(labelID, name string, color int32) appstate.PatchInfo {
	return appstate.BuildLabelEdit(labelID, name, color, false)
}

func buildLabelDeletePatch(labelID, name string) appstate.PatchInfo {
	return appstate.BuildLabelEdit(labelID, name, 0, true)
}

func buildLabelChatPatch(chat types.JID, labelID string, labeled bool) appstate.PatchInfo {
	return appstate.BuildLabelChat(chat, labelID, labeled)
}

func sendLabelPatch(ctx context.Context, client *whatsmeow.Client, patch appstate.PatchInfo, fields logrus.Fields) error {
	if err := client.SendAppState(ctx, patch); err != nil {
		logrus.WithError(err).WithFields(fields).Error("Failed to send label app state")

		return pkgError.Upstream(err.Error())
	}

	return nil
}

func (serviceLabel) CreateLabel(ctx context.Context, request domainLabel.CreateLabelRequest) (response domainLabel.CreateLabelResponse, err error) {
	if err = validations.ValidateCreateLabel(ctx, &request); err != nil {
		return response, err
	}
	client, deviceID, err := labelClient(ctx)
	if err != nil {
		return response, err
	}

	labelID, err := nextLabelID(
		func() (int64, bool) { return whatsapp.KnownMaxLabelID(client) },
		func() error { return whatsapp.SyncLabelsNow(ctx, client, deviceID) },
	)
	if err != nil {
		return response, err
	}

	if err = sendLabelPatch(ctx, client, buildLabelUpsertPatch(labelID, request.Name, request.Color),
		logrus.Fields{"label_id": labelID}); err != nil {
		return response, err
	}
	// O eco do evento chega depois; registra já para a próxima criação não repetir o id.
	whatsapp.NoteLabelID(client, labelID)
	response.LabelID = labelID

	return response, nil
}

func (serviceLabel) UpdateLabel(ctx context.Context, request domainLabel.UpdateLabelRequest) error {
	if err := validations.ValidateUpdateLabel(ctx, &request); err != nil {
		return err
	}
	client, _, err := labelClient(ctx)
	if err != nil {
		return err
	}

	return sendLabelPatch(ctx, client, buildLabelUpsertPatch(request.LabelID, request.Name, request.Color),
		logrus.Fields{"label_id": request.LabelID})
}

func (serviceLabel) DeleteLabel(ctx context.Context, request domainLabel.DeleteLabelRequest) error {
	if err := validations.ValidateDeleteLabel(ctx, &request); err != nil {
		return err
	}
	client, _, err := labelClient(ctx)
	if err != nil {
		return err
	}

	return sendLabelPatch(ctx, client, buildLabelDeletePatch(request.LabelID, request.Name),
		logrus.Fields{"label_id": request.LabelID})
}

func (serviceLabel) LabelChat(ctx context.Context, request domainLabel.LabelChatRequest) error {
	if err := validations.ValidateLabelChat(ctx, &request); err != nil {
		return err
	}
	client, _, err := labelClient(ctx)
	if err != nil {
		return err
	}
	chat, err := types.ParseJID(request.ChatJID)
	if err != nil {
		return pkgError.ValidationError("chat_jid: invalid JID.")
	}

	return sendLabelPatch(ctx, client, buildLabelChatPatch(chat.ToNonAD(), request.LabelID, request.Labeled),
		logrus.Fields{"label_id": request.LabelID, "chat_jid": request.ChatJID, "labeled": request.Labeled})
}
