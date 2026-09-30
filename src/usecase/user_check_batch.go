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
// asked in a single call, each Brazilian mobile number together with its other ninth-digit
// form; invalid entries never reach WhatsApp.
func runCheckBatch(ctx context.Context, checker phoneChecker, raw []string) domainUser.CheckBatchResponse {
	items := make(domainUser.CheckBatchResponse, len(raw))
	var ask []string
	requested := map[string]bool{} // entries as the client sent them
	askedSet := map[string]bool{}  // requested numbers plus their variants
	variants := map[string]string{}
	add := func(digits string) {
		if !askedSet[digits] {
			askedSet[digits] = true
			ask = append(ask, "+"+digits)
		}
	}
	for i, entry := range raw {
		digits, ok := validations.NormalizeBatchPhone(entry)
		if !ok {
			items[i] = errorItem(strings.TrimSpace(entry), checkErrInvalidNumber)
			continue
		}
		items[i].Query = digits
		if requested[digits] {
			continue
		}
		requested[digits] = true
		add(digits)
		if alt := brVariant(digits); alt != "" {
			variants[digits] = alt
			add(alt)
		}
	}
	if len(ask) == 0 {
		return items
	}

	resolved := make(map[string]domainUser.CheckBatchItem, len(requested))
	answers, err := checker.IsOnWhatsApp(ctx, ask)
	// whatsmeow can return the full answer together with a "failed to store LID mappings"
	// error; every earlier failure returns no answers. Keep what WhatsApp answered.
	if err != nil && len(answers) == 0 {
		logrus.Warnf("Batch user check call failed: %v", err)
		for digits := range requested {
			resolved[digits] = errorItem(digits, checkErrUpstream)
		}
	} else {
		if err != nil {
			logrus.Warnf("Batch user check answered with an error: %v", err)
		}
		byDigits := map[string]types.IsOnWhatsAppResponse{}
		unmatchedIn := 0
		for _, answer := range answers {
			digits := answerDigits(answer, askedSet)
			if digits == "" {
				if answer.IsIn {
					unmatchedIn++
				}
				continue
			}
			if previous, seen := byDigits[digits]; !seen || (answer.IsIn && !previous.IsIn) {
				byDigits[digits] = answer
			}
		}
		for digits := range requested {
			primary, hasPrimary := byDigits[digits]
			alt, hasAlt := byDigits[variants[digits]]
			switch {
			case hasPrimary && primary.IsIn:
				resolved[digits] = existsItem(ctx, checker, digits, primary)
			case hasAlt && alt.IsIn:
				resolved[digits] = existsItem(ctx, checker, digits, alt)
			case unmatchedIn > 0 && !hasPrimary && !hasAlt:
				// An "in" answer that matches no requested number means the answer cannot be
				// trusted to say which numbers are absent: never call those not_exists.
				resolved[digits] = errorItem(digits, checkErrUpstream)
			default:
				resolved[digits] = domainUser.CheckBatchItem{Query: digits, Status: checkStatusNotExists}
			}
		}
		if unmatchedIn > 0 {
			logrus.Warnf("Batch user check: %d answers matched no requested number", unmatchedIn)
		}
	}

	for i := range items {
		if items[i].Status == "" {
			items[i] = resolved[items[i].Query]
		}
	}
	return items
}

// brVariant returns the other ninth-digit form of a Brazilian mobile number, or "" when the
// number has none. A 13-digit number (55, DDD, 9, eight digits) also exists without the 9; a
// 12-digit one whose subscriber part starts with 6 to 9 also exists with it. WhatsApp does not
// fix a missing or extra 9: older accounts are registered without it.
func brVariant(digits string) string {
	if !strings.HasPrefix(digits, "55") {
		return ""
	}
	switch len(digits) {
	case 13:
		if digits[4] == '9' {
			return digits[:4] + digits[5:]
		}
	case 12:
		if c := digits[4]; c >= '6' && c <= '9' {
			return digits[:4] + "9" + digits[4:]
		}
	}
	return ""
}

// answerDigits finds which requested number an answer belongs to: by the query WhatsApp
// echoes, else by the phone number or phone JID it returns. "" when none matches.
func answerDigits(answer types.IsOnWhatsAppResponse, asked map[string]bool) string {
	candidates := []string{onlyDigits(answer.Query), onlyDigits(answer.PhoneNumber.User)}
	if answer.JID.Server == types.DefaultUserServer {
		candidates = append(candidates, onlyDigits(answer.JID.User))
	}
	for _, digits := range candidates {
		if digits != "" && asked[digits] {
			return digits
		}
	}
	return ""
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
