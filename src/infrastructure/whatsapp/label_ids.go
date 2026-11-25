package whatsapp

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"go.mau.fi/whatsmeow"
)

// labelMaxIDs guarda, por cliente, o maior id de etiqueta visto em qualquer label.edit
// (ao vivo ou em sincronização completa). Ids são numéricos e nunca reaproveitados (D7454).
var labelMaxIDs sync.Map // *whatsmeow.Client -> *atomic.Int64

// NoteLabelID registra um id de etiqueta visto. Id não numérico é ignorado.
func NoteLabelID(client *whatsmeow.Client, labelID string) {
	if client == nil {
		return
	}
	id, err := strconv.ParseInt(labelID, 10, 64)
	if err != nil || id < 0 {
		return
	}
	v, _ := labelMaxIDs.LoadOrStore(client, &atomic.Int64{})
	counter := v.(*atomic.Int64)
	for {
		cur := counter.Load()
		if id <= cur || counter.CompareAndSwap(cur, id) {
			return
		}
	}
}

// MarkLabelIDsKnown marca o aparelho como sincronizado, mesmo sem nenhuma etiqueta (max 0).
func MarkLabelIDsKnown(client *whatsmeow.Client) {
	if client != nil {
		labelMaxIDs.LoadOrStore(client, &atomic.Int64{})
	}
}

// KnownMaxLabelID devolve o maior id de etiqueta conhecido; false se nenhum foi visto ainda.
func KnownMaxLabelID(client *whatsmeow.Client) (int64, bool) {
	if client == nil {
		return 0, false
	}
	v, ok := labelMaxIDs.Load(client)
	if !ok {
		return 0, false
	}

	return v.(*atomic.Int64).Load(), true
}

// SyncLabelsNow roda a sincronização completa e espera terminar, sem esperar o webhook de fim.
func SyncLabelsNow(ctx context.Context, client *whatsmeow.Client, deviceID string) error {
	run, err := beginLabelSync(client)
	if err != nil {
		return err
	}
	notify := func(c context.Context, id string, payload map[string]any) {
		safego.Go("label_sync_notify", func() { sendLabelSyncCompleted(c, id, payload) })
	}

	if err := runLabelSync(ctx, client, deviceID, run, fetchLabelAppState, notify); err != nil {
		return err
	}
	MarkLabelIDsKnown(client)

	return nil
}
