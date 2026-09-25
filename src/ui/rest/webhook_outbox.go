package rest

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/webhookoutbox"
	"github.com/gofiber/fiber/v3"
)

// Fork (elphant): operations API of the durable webhook outbox. Every route
// answers 404 while WHATSAPP_WEBHOOK_DELIVERY is direct.
type WebhookOutbox struct {
	Outbox func() *webhookoutbox.Outbox
}

func InitRestWebhookOutbox(app fiber.Router, outbox func() *webhookoutbox.Outbox) {
	handler := WebhookOutbox{Outbox: outbox}
	app.Get("/webhooks/stats", handler.Stats)
	app.Get("/webhooks/deliveries", handler.ListDeliveries)
	app.Get("/webhooks/deliveries/:event_id", handler.GetDelivery)
	app.Post("/webhooks/deliveries/:event_id/redeliver", handler.Redeliver)
	app.Post("/webhooks/replay", handler.Replay)
}

func (handler WebhookOutbox) Stats(c fiber.Ctx) error {
	outbox := handler.Outbox()
	if outbox == nil {
		return outboxOff(c)
	}
	stats, err := outbox.Store().Stats(c.Context())
	if err != nil {
		return outboxFailure(c, err)
	}
	return c.JSON(utils.ResponseData{Status: http.StatusOK, Code: "SUCCESS", Message: "Webhook outbox stats", Results: stats})
}

func (handler WebhookOutbox) ListDeliveries(c fiber.Ctx) error {
	outbox := handler.Outbox()
	if outbox == nil {
		return outboxOff(c)
	}
	status := c.Query("status")
	switch status {
	case "", webhookoutbox.StatusPending, webhookoutbox.StatusDelivered, webhookoutbox.StatusDead:
	default:
		return badOutboxRequest(c, "status must be pending, delivered or dead")
	}
	limit := webhookoutbox.DefaultListLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > webhookoutbox.MaxListLimit {
			return badOutboxRequest(c, "limit must be between 1 and 500")
		}
		limit = n
	}
	var beforeID int64
	if raw := c.Query("before_id"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			return badOutboxRequest(c, "before_id must be a positive integer")
		}
		beforeID = n
	}
	rows, err := outbox.Store().List(c.Context(), status, limit, beforeID)
	if err != nil {
		return outboxFailure(c, err)
	}
	return c.JSON(utils.ResponseData{Status: http.StatusOK, Code: "SUCCESS", Message: "Webhook deliveries", Results: rows})
}

func (handler WebhookOutbox) GetDelivery(c fiber.Ctx) error {
	outbox := handler.Outbox()
	if outbox == nil {
		return outboxOff(c)
	}
	row, err := outbox.Store().Get(c.Context(), c.Params("event_id"))
	if err != nil {
		return outboxFailure(c, err)
	}
	if row == nil {
		return deliveryNotFound(c)
	}
	return c.JSON(utils.ResponseData{Status: http.StatusOK, Code: "SUCCESS", Message: "Webhook delivery", Results: row})
}

func (handler WebhookOutbox) Redeliver(c fiber.Ctx) error {
	outbox := handler.Outbox()
	if outbox == nil {
		return outboxOff(c)
	}
	row, err := outbox.Redeliver(c.Context(), c.Params("event_id"))
	if errors.Is(err, webhookoutbox.ErrAlreadyPending) {
		return c.Status(http.StatusConflict).JSON(utils.ResponseData{Status: http.StatusConflict, Code: "DELIVERY_PENDING", Message: "the delivery is still queued; its worker will send it"})
	}
	if err != nil {
		return outboxFailure(c, err)
	}
	if row == nil {
		return deliveryNotFound(c)
	}
	row.Body = nil
	return c.JSON(utils.ResponseData{Status: http.StatusOK, Code: "SUCCESS", Message: "Webhook delivery queued again", Results: row})
}

func (handler WebhookOutbox) Replay(c fiber.Ctx) error {
	outbox := handler.Outbox()
	if outbox == nil {
		return outboxOff(c)
	}
	since, err := time.Parse(time.RFC3339, c.Query("since"))
	if err != nil {
		return badOutboxRequest(c, "since must be an RFC3339 time, e.g. 2026-09-25T12:00:00Z")
	}
	n, err := outbox.Replay(c.Context(), since, c.Query("url"))
	if err != nil {
		return outboxFailure(c, err)
	}
	return c.JSON(utils.ResponseData{Status: http.StatusOK, Code: "SUCCESS", Message: "Webhook deliveries queued again", Results: map[string]any{"requeued": n}})
}

func outboxOff(c fiber.Ctx) error {
	return c.Status(http.StatusNotFound).JSON(utils.ResponseData{
		Status: http.StatusNotFound, Code: "NOT_FOUND",
		Message: "durable webhook delivery is off (WHATSAPP_WEBHOOK_DELIVERY=direct)",
	})
}

func deliveryNotFound(c fiber.Ctx) error {
	return c.Status(http.StatusNotFound).JSON(utils.ResponseData{Status: http.StatusNotFound, Code: "DELIVERY_NOT_FOUND", Message: "no webhook delivery with this event_id"})
}

func badOutboxRequest(c fiber.Ctx, message string) error {
	return c.Status(http.StatusBadRequest).JSON(utils.ResponseData{Status: http.StatusBadRequest, Code: "BAD_REQUEST", Message: message})
}

func outboxFailure(c fiber.Ctx, err error) error {
	return c.Status(http.StatusInternalServerError).JSON(utils.ResponseData{Status: http.StatusInternalServerError, Code: "INTERNAL_SERVER_ERROR", Message: err.Error()})
}
