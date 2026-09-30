// Package lid holds the DTOs of the LID and phone mapping endpoints (fork: elphant).
package lid

import "context"

// PNItem answers a phone lookup; LID is null when the pair is unknown.
type PNItem struct {
	PN  string  `json:"pn"`
	LID *string `json:"lid"`
}

// LIDItem answers a LID lookup; PN is null when the pair is unknown.
type LIDItem struct {
	LID string  `json:"lid"`
	PN  *string `json:"pn"`
}

type LookupRequest struct {
	PNs  []string `json:"pns"`
	LIDs []string `json:"lids"`
}

type LookupResponse struct {
	PNs  []PNItem  `json:"pns"`
	LIDs []LIDItem `json:"lids"`
}

type ListRequest struct {
	Limit int    `query:"limit"`
	After string `query:"after"`
}

type ListItem struct {
	LID string `json:"lid"`
	PN  string `json:"pn"`
}

// ListResponse pages through every pair the gateway knows. Next is null on the last page.
type ListResponse struct {
	Items []ListItem `json:"items"`
	Next  *string    `json:"next"`
}

type ILIDUsecase interface {
	PNToLID(ctx context.Context, phone string) (PNItem, error)
	LIDToPN(ctx context.Context, lid string) (LIDItem, error)
	Lookup(ctx context.Context, request LookupRequest) (LookupResponse, error)
	List(ctx context.Context, request ListRequest) (ListResponse, error)
}
