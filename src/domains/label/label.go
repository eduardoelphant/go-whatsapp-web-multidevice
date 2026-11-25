package label

import "context"

// ILabelUsecase define as operações de etiquetas do app WhatsApp Business.
type ILabelUsecase interface {
	// SyncLabels dispara a sincronização completa das etiquetas do aparelho em segundo plano.
	SyncLabels(ctx context.Context) (response SyncLabelsResponse, err error)
}

type SyncLabelsResponse struct {
	SyncID string `json:"sync_id"`
}
