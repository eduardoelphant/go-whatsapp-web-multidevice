package label

import "context"

// ILabelUsecase define as operações de etiquetas do app WhatsApp Business.
type ILabelUsecase interface {
	// SyncLabels dispara a sincronização completa das etiquetas do aparelho em segundo plano.
	SyncLabels(ctx context.Context) (response SyncLabelsResponse, err error)
	// CreateLabel cria uma etiqueta com id acima do maior conhecido pelo aparelho.
	CreateLabel(ctx context.Context, request CreateLabelRequest) (response CreateLabelResponse, err error)
	// UpdateLabel renomeia ou troca a cor de uma etiqueta.
	UpdateLabel(ctx context.Context, request UpdateLabelRequest) (err error)
	// DeleteLabel exclui uma etiqueta.
	DeleteLabel(ctx context.Context, request DeleteLabelRequest) (err error)
	// LabelChat aplica ou remove a etiqueta de uma conversa.
	LabelChat(ctx context.Context, request LabelChatRequest) (err error)
}

type SyncLabelsResponse struct {
	SyncID string `json:"sync_id"`
}

type CreateLabelRequest struct {
	Name  string `json:"name"`
	Color int32  `json:"color"`
}

type CreateLabelResponse struct {
	LabelID string `json:"label_id"`
}

type UpdateLabelRequest struct {
	LabelID string `json:"label_id" uri:"label_id"`
	Name    string `json:"name"`
	Color   int32  `json:"color"`
}

type DeleteLabelRequest struct {
	LabelID string `json:"label_id" uri:"label_id"`
	Name    string `json:"name"`
}

type LabelChatRequest struct {
	LabelID string `json:"label_id" uri:"label_id"`
	ChatJID string `json:"chat_jid"`
	Labeled bool   `json:"labeled"`
}
