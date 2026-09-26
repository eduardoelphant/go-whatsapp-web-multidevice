package usecase

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	chatstorageRepo "github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/sqlite"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/websocket"
)

// Fork (elphant): the default device (EnsureDefault) lives in the manager without a
// row in the devices registry; setting its webhook used to fail with sql.ErrNoRows.
func TestSetDeviceWebhookConfigRegistersTheDefaultDevice(t *testing.T) {
	db, err := sql.Open(sqlite.DriverName, sqlite.FormatChatStorageURI("file:"+filepath.Join(t.TempDir(), "chat.db"), true, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := chatstorageRepo.NewStorageRepository(db)
	if err := repo.InitializeSchema(); err != nil {
		t.Fatal(err)
	}

	// The usecase announces the change on the websocket hub, which no test runs.
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-websocket.Broadcast:
			case <-done:
				return
			}
		}
	}()

	manager := whatsapp.NewDeviceManager(nil, nil, repo)
	manager.EnsureDefault(whatsapp.NewDeviceInstance("5511999999999:17@s.whatsapp.net", nil, repo))
	svc := &serviceDevice{manager: manager}

	url := "https://crm.test/v1/webhook/whatsapp_gowa/ch1"
	err = svc.SetDeviceWebhookConfig(context.Background(), "5511999999999:17@s.whatsapp.net", &chatstorage.DeviceWebhookConfig{
		WebhookURL:    &url,
		WebhookSecret: "secret",
		WebhookEvents: "message,session.status",
	})
	if err != nil {
		t.Fatalf("SetDeviceWebhookConfig: %v", err)
	}

	got, err := repo.GetDeviceWebhookConfig("5511999999999:17@s.whatsapp.net")
	if err != nil || got == nil || got.WebhookURL == nil || *got.WebhookURL != url || got.WebhookSecret != "secret" {
		t.Fatalf("stored config = %+v, %v", got, err)
	}
}
