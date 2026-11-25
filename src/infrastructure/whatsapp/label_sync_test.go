package whatsapp

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func labelEditEvt() *events.AppState {
	return &events.AppState{
		Index: []string{"label_edit", "9"},
		SyncActionValue: &waSyncAction.SyncActionValue{
			LabelEditAction: &waSyncAction.LabelEditAction{Name: proto.String("VIP")},
		},
	}
}

func labelLinkEvt() *events.AppState {
	return &events.AppState{
		Index: []string{"label_jid", "9", "120363424051089958@g.us"},
		SyncActionValue: &waSyncAction.SyncActionValue{
			LabelAssociationAction: &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(true)},
		},
	}
}

func TestLabelWebhookBodyLiveEventHasNoSyncFields(t *testing.T) {
	client := &whatsmeow.Client{}
	name, body := labelWebhookBody(context.Background(), labelEditEvt(), "dev", client)
	if name != eventTypeLabelEdit {
		t.Fatalf("event = %q", name)
	}
	payload := body["payload"].(map[string]any)
	if _, ok := payload["full_sync"]; ok {
		t.Fatal("live event must not carry full_sync")
	}
	if _, ok := payload["sync_id"]; ok {
		t.Fatal("live event must not carry sync_id")
	}
}

func TestLabelWebhookBodyDuringFullSyncCarriesSyncID(t *testing.T) {
	client := &whatsmeow.Client{}
	run, err := beginLabelSync(client)
	if err != nil {
		t.Fatal(err)
	}
	defer endLabelSync(client)

	_, body := labelWebhookBody(context.Background(), labelEditEvt(), "dev", client)
	payload := body["payload"].(map[string]any)
	if payload["full_sync"] != true || payload["sync_id"] != run.id {
		t.Fatalf("payload = %#v", payload)
	}
	labelWebhookBody(context.Background(), labelLinkEvt(), "dev", client)
	if run.labels.Load() != 1 || run.links.Load() != 1 {
		t.Fatalf("counts labels=%d links=%d", run.labels.Load(), run.links.Load())
	}
}

func TestNonLabelAppStateIsNotCountedNorForwarded(t *testing.T) {
	client := &whatsmeow.Client{}
	run, _ := beginLabelSync(client)
	defer endLabelSync(client)

	for _, idx := range [][]string{{"mute", "x@g.us"}, {"label_message", "9", "x@g.us", "id", "0", "0"}} {
		evt := &events.AppState{Index: idx, SyncActionValue: &waSyncAction.SyncActionValue{
			LabelAssociationAction: &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(true)},
		}}
		if isLabelAppState(evt) {
			t.Fatalf("%v must not be a label event", idx)
		}
	}
	if run.labels.Load() != 0 || run.links.Load() != 0 {
		t.Fatal("counts must stay zero")
	}
}

func TestBeginLabelSyncRejectsConcurrentRun(t *testing.T) {
	client := &whatsmeow.Client{}
	if _, err := beginLabelSync(client); err != nil {
		t.Fatal(err)
	}
	if _, err := beginLabelSync(client); !errors.Is(err, ErrLabelSyncRunning) {
		t.Fatalf("err = %v", err)
	}
	endLabelSync(client)
	if _, err := beginLabelSync(client); err != nil {
		t.Fatalf("after end: %v", err)
	}
	endLabelSync(client)
}

func TestRunLabelSyncEmitsCompletedWithCountsAndRestoresFlag(t *testing.T) {
	client := &whatsmeow.Client{}
	run, _ := beginLabelSync(client)

	fetch := func(ctx context.Context, c *whatsmeow.Client) error {
		if !c.EmitAppStateEventsOnFullSync {
			t.Error("flag must be on during fetch")
		}
		handleAppState(ctx, labelEditEvt(), "dev", c)
		handleAppState(ctx, labelEditEvt(), "dev", c)
		handleAppState(ctx, labelLinkEvt(), "dev", c)
		return nil
	}
	var got map[string]any
	notify := func(ctx context.Context, deviceID string, payload map[string]any) { got = payload }

	if err := runLabelSync(context.Background(), client, "dev", run, fetch, notify); err != nil {
		t.Fatal(err)
	}
	if client.EmitAppStateEventsOnFullSync {
		t.Fatal("flag must be restored")
	}
	if got["sync_id"] != run.id || got["ok"] != true || got["labels"] != int64(2) || got["links"] != int64(1) {
		t.Fatalf("completed = %#v", got)
	}
	if labelSyncFor(client) != nil {
		t.Fatal("run must be released")
	}
}

func TestRunLabelSyncFetchErrorStillNotifiesWithOkFalse(t *testing.T) {
	client := &whatsmeow.Client{EmitAppStateEventsOnFullSync: false}
	run, _ := beginLabelSync(client)
	var got map[string]any
	err := runLabelSync(context.Background(), client, "dev", run,
		func(context.Context, *whatsmeow.Client) error { return errors.New("boom") },
		func(_ context.Context, _ string, p map[string]any) { got = p })
	if err == nil || got == nil || got["ok"] != false || got["error"] != "boom" || got["sync_id"] != run.id {
		t.Fatalf("err=%v got=%#v", err, got)
	}
	if client.EmitAppStateEventsOnFullSync || labelSyncFor(client) != nil {
		t.Fatal("state must be restored")
	}
}

func TestLabelEditEventTracksMaxID(t *testing.T) {
	client := &whatsmeow.Client{}
	if _, ok := KnownMaxLabelID(client); ok {
		t.Fatal("no id must be known yet")
	}
	for _, id := range []string{"3", "17", "5", "abc"} {
		evt := labelEditEvt()
		evt.Index = []string{"label_edit", id}
		labelWebhookBody(context.Background(), evt, "dev", client)
	}
	if n, ok := KnownMaxLabelID(client); !ok || n != 17 {
		t.Fatalf("max = %d ok=%v", n, ok)
	}
	// Associação não conta como etiqueta.
	NoteLabelID(client, "9")
	if n, _ := KnownMaxLabelID(client); n != 17 {
		t.Fatalf("max = %d", n)
	}
}
