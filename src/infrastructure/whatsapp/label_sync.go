package whatsapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
)

const eventTypeLabelSyncCompleted = "label.sync.completed"

// ErrLabelSyncRunning indica que o aparelho já tem uma sincronização de etiquetas em curso.
var ErrLabelSyncRunning = errors.New("label sync already running for this device")

type labelSyncRun struct {
	id     string
	labels atomic.Int64
	links  atomic.Int64
	wg     sync.WaitGroup
}

// labelSyncs guarda a sincronização em curso por cliente (o JID é compartilhado entre slots irmãos).
var labelSyncs sync.Map // *whatsmeow.Client -> *labelSyncRun

func labelSyncFor(client *whatsmeow.Client) *labelSyncRun {
	if client == nil {
		return nil
	}
	if v, ok := labelSyncs.Load(client); ok {
		return v.(*labelSyncRun)
	}

	return nil
}

func beginLabelSync(client *whatsmeow.Client) (*labelSyncRun, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	run := &labelSyncRun{id: hex.EncodeToString(buf)}
	if _, loaded := labelSyncs.LoadOrStore(client, run); loaded {
		return nil, ErrLabelSyncRunning
	}

	return run, nil
}

func endLabelSync(client *whatsmeow.Client) {
	labelSyncs.Delete(client)
}

type labelFetchFunc func(ctx context.Context, client *whatsmeow.Client) error

type labelNotifyFunc func(ctx context.Context, deviceID string, payload map[string]any)

// StartLabelSync inicia em segundo plano a sincronização completa das etiquetas e devolve o sync_id.
func StartLabelSync(client *whatsmeow.Client, deviceID string) (string, error) {
	if client == nil {
		return "", errors.New("client is nil")
	}
	run, err := beginLabelSync(client)
	if err != nil {
		return "", err
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := runLabelSync(ctx, client, deviceID, run, fetchLabelAppState, sendLabelSyncCompleted); err != nil {
			logrus.WithError(err).Errorf("Label full sync %s failed", run.id)
		}
	}()

	return run.id, nil
}

// fetchLabelAppState: etiquetas (label_edit e label_jid) moram no patch regular.
func fetchLabelAppState(ctx context.Context, client *whatsmeow.Client) error {
	return client.FetchAppState(ctx, appstate.WAPatchRegular, true, false)
}

func runLabelSync(ctx context.Context, client *whatsmeow.Client, deviceID string, run *labelSyncRun, fetch labelFetchFunc, notify labelNotifyFunc) error {
	defer endLabelSync(client)

	previous := client.EmitAppStateEventsOnFullSync
	client.EmitAppStateEventsOnFullSync = true
	err := fetch(ctx, client)
	client.EmitAppStateEventsOnFullSync = previous
	if err != nil {
		run.wg.Wait()

		return err
	}

	// Os eventos saem em goroutines: espera todos antes de fechar a contagem.
	run.wg.Wait()
	notify(context.WithoutCancel(ctx), deviceID, map[string]any{
		"sync_id": run.id,
		"labels":  run.labels.Load(),
		"links":   run.links.Load(),
	})

	return nil
}

func sendLabelSyncCompleted(ctx context.Context, deviceID string, payload map[string]any) {
	body := map[string]any{
		"event":     eventTypeLabelSyncCompleted,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"payload":   payload,
	}
	if deviceID != "" {
		body["device_id"] = deviceID
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := forwardPayloadToConfiguredWebhooks(wctx, body, eventTypeLabelSyncCompleted); err != nil {
		logrus.WithError(err).Error("Failed to forward label.sync.completed")
	}
}
