package usecase

import (
	"context"

	domainUser "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
)

func (service serviceUser) IsOnWhatsAppBatch(ctx context.Context, request domainUser.CheckBatchRequest) (domainUser.CheckBatchResponse, error) {
	return nil, nil
}
