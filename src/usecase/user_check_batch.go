package usecase

import (
	"context"
	"strings"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainUser "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

const (
	checkStatusExists     = "exists"
	checkStatusNotExists  = "not_exists"
	checkStatusError      = "error"
	checkErrInvalidNumber = "invalid_number"
	checkErrUpstream      = "upstream"
	checkBatchTimeout     = 20 * time.Second
)

// userCheckPacer spaces batch checks per device.
var userCheckPacer = newCheckPacer()

// phoneChecker is the part of whatsmeow the batch check needs; tests use a fake.
type phoneChecker interface {
	IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
	LIDForPN(ctx context.Context, pn types.JID) (types.JID, error)
}

type whatsmeowChecker struct{ client *whatsmeow.Client }

func (c whatsmeowChecker) IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	return c.client.IsOnWhatsApp(ctx, phones)
}

func (c whatsmeowChecker) LIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	if c.client.Store == nil || c.client.Store.LIDs == nil {
		return types.EmptyJID, nil
	}
	return c.client.Store.LIDs.GetLIDForPN(ctx, pn)
}

func (service serviceUser) IsOnWhatsAppBatch(ctx context.Context, request domainUser.CheckBatchRequest) (domainUser.CheckBatchResponse, error) {
	if err := validations.ValidateCheckBatch(ctx, request); err != nil {
		return nil, err
	}
	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return nil, pkgError.ErrWaCLI
	}
	utils.MustLogin(client)

	key := "default"
	if inst, ok := whatsapp.DeviceFromContext(ctx); ok && inst != nil {
		key = inst.ID()
	}
	interval := time.Duration(config.WhatsappUserCheckMinIntervalMs) * time.Millisecond
	if interval < 0 {
		interval = 0
	}

	var items domainUser.CheckBatchResponse
	started := time.Now()
	err := userCheckPacer.run(ctx, key, interval, func() error {
		callCtx, cancel := context.WithTimeout(ctx, checkBatchTimeout)
		defer cancel()
		items = runCheckBatch(callCtx, whatsmeowChecker{client: client}, request.Phones)
		return nil
	})
	if err != nil {
		return nil, err
	}

	counts := map[string]int{}
	for _, item := range items {
		counts[item.Status]++
	}
	logrus.Infof("Batch user check: entries=%d exists=%d not_exists=%d error=%d duration=%s",
		len(items), counts[checkStatusExists], counts[checkStatusNotExists], counts[checkStatusError], time.Since(started).Round(time.Millisecond))
	return items, nil
}

// runCheckBatch builds one item per raw entry, in order. Valid entries are deduplicated and
// asked in a single call; invalid entries never reach WhatsApp.
func runCheckBatch(ctx context.Context, checker phoneChecker, raw []string) domainUser.CheckBatchResponse {
	items := make(domainUser.CheckBatchResponse, len(raw))
	var ask []string
	asked := map[string]bool{}
	for i, entry := range raw {
		digits, ok := validations.NormalizeBatchPhone(entry)
		if !ok {
			items[i] = errorItem(strings.TrimSpace(entry), checkErrInvalidNumber)
			continue
		}
		items[i].Query = digits
		if !asked[digits] {
			asked[digits] = true
			ask = append(ask, "+"+digits)
		}
	}
	if len(ask) == 0 {
		return items
	}

	resolved := make(map[string]domainUser.CheckBatchItem, len(ask))
	answers, err := checker.IsOnWhatsApp(ctx, ask)
	if err != nil {
		logrus.Warnf("Batch user check call failed: %v", err)
		for digits := range asked {
			resolved[digits] = errorItem(digits, checkErrUpstream)
		}
	} else {
		for digits := range asked {
			resolved[digits] = domainUser.CheckBatchItem{Query: digits, Status: checkStatusNotExists}
		}
		for _, answer := range answers {
			digits := onlyDigits(answer.Query)
			if _, wanted := asked[digits]; !wanted || !answer.IsIn {
				continue
			}
			resolved[digits] = existsItem(ctx, checker, digits, answer)
		}
	}

	for i := range items {
		if items[i].Status == "" {
			items[i] = resolved[items[i].Query]
		}
	}
	return items
}

func existsItem(ctx context.Context, checker phoneChecker, digits string, answer types.IsOnWhatsAppResponse) domainUser.CheckBatchItem {
	item := domainUser.CheckBatchItem{Query: digits, Status: checkStatusExists}

	pn := answer.PhoneNumber
	if pn.IsEmpty() && answer.JID.Server == types.DefaultUserServer {
		pn = answer.JID
	}
	if !pn.IsEmpty() {
		s := pn.ToNonAD().String()
		item.PN = &s
	}

	lid := types.EmptyJID
	if answer.JID.Server == types.HiddenUserServer {
		lid = answer.JID
	} else if !pn.IsEmpty() {
		if mapped, err := checker.LIDForPN(ctx, pn.ToNonAD()); err == nil {
			lid = mapped
		}
	}
	if !lid.IsEmpty() {
		s := lid.ToNonAD().String()
		item.LID = &s
	}

	if answer.VerifiedName != nil && answer.VerifiedName.Details != nil {
		if name := answer.VerifiedName.Details.GetVerifiedName(); name != "" {
			item.VerifiedName = &name
		}
	}
	return item
}

func errorItem(query, code string) domainUser.CheckBatchItem {
	return domainUser.CheckBatchItem{Query: query, Status: checkStatusError, Error: &code}
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
