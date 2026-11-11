package usecase

import (
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	domainGroup "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/group"
)

// participantStatuses converte o retorno do WhatsApp em status por participante.
// O próprio número (own) fica de fora: o servidor o devolve como membro do grupo criado.
func participantStatuses(participants []types.GroupParticipant, own types.JID) []domainGroup.ParticipantStatus {
	ownUser := own.ToNonAD().User
	result := make([]domainGroup.ParticipantStatus, 0, len(participants))

	for _, participant := range participants {
		if ownUser != "" && (participant.JID.ToNonAD().User == ownUser || participant.PhoneNumber.ToNonAD().User == ownUser) {
			continue
		}

		status := domainGroup.ParticipantStatus{
			Participant: participant.JID.String(),
			Status:      "success",
			Message:     "Action success",
		}
		if !participant.PhoneNumber.IsEmpty() {
			status.PhoneNumber = participant.PhoneNumber.String()
		}

		switch {
		case participant.Error == 0:
		case participant.Error == 403 && participant.AddRequest != nil:
			status.Status = "error"
			status.Message = "Failed to add participant"
			status.Code = 403
		default:
			status.Status = "error"
			status.Message = fmt.Sprintf("Failed to add participant (code %d)", participant.Error)
			status.Code = participant.Error
		}

		result = append(result, status)
	}

	return result
}

// requestParticipantStatuses mapeia aprovar e recusar pedidos de entrada; Code leva o erro do WhatsApp (409 = já resolvido).
func requestParticipantStatuses(participants []types.GroupParticipant, action whatsmeow.ParticipantRequestChange) []domainGroup.ParticipantStatus {
	result := make([]domainGroup.ParticipantStatus, 0, len(participants))
	for _, participant := range participants {
		status := domainGroup.ParticipantStatus{
			Participant: participant.JID.String(),
			Status:      "success",
			Message:     fmt.Sprintf("Action %s success", action),
		}
		if participant.Error != 0 {
			status.Status = "error"
			status.Message = fmt.Sprintf("Action %s failed (code %d)", action, participant.Error)
			status.Code = participant.Error
		}
		result = append(result, status)
	}
	return result
}
