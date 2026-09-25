package usecase

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

type mediaStreamRepo struct {
	domainChatStorage.IChatStorageRepository
	msg *domainChatStorage.Message
}

func (r mediaStreamRepo) GetMessageByIDAndDevice(_, _ string) (*domainChatStorage.Message, error) {
	return r.msg, nil
}

func mediaStreamCtx() context.Context {
	jid := types.NewADJID("5511900000009", 0, 12)
	client := &whatsmeow.Client{Store: &store.Device{ID: &jid}}
	return whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("slot", client, nil))
}

func withDownload(t *testing.T, fn func(ctx context.Context, client *whatsmeow.Client, msg whatsmeow.DownloadableMessage, file *os.File) error) {
	t.Helper()
	previous := mediaStreamDownloadFn
	mediaStreamDownloadFn = fn
	t.Cleanup(func() { mediaStreamDownloadFn = previous })
}

var storedImage = &domainChatStorage.Message{
	ID: "IMG1", MediaType: "image", DirectPath: "/v/t62/abc", MediaKey: []byte{1}, FileSHA256: []byte{2}, FileEncSHA256: []byte{3}, FileLength: 4,
}

func TestStreamMediaNotFound(t *testing.T) {
	for name, msg := range map[string]*domainChatStorage.Message{"missing": nil, "no media": {ID: "TXT1"}} {
		t.Run(name, func(t *testing.T) {
			svc := serviceMessage{chatStorageRepo: mediaStreamRepo{msg: msg}}
			if _, err := svc.StreamMedia(mediaStreamCtx(), "X"); !errors.Is(err, domainMessage.ErrMediaNotFound) {
				t.Fatalf("err = %v, want ErrMediaNotFound", err)
			}
		})
	}
}

func TestStreamMediaGone(t *testing.T) {
	withDownload(t, func(context.Context, *whatsmeow.Client, whatsmeow.DownloadableMessage, *os.File) error {
		return whatsmeow.ErrMediaDownloadFailedWith410
	})
	svc := serviceMessage{chatStorageRepo: mediaStreamRepo{msg: storedImage}}
	if _, err := svc.StreamMedia(mediaStreamCtx(), "IMG1"); !errors.Is(err, domainMessage.ErrMediaGone) {
		t.Fatalf("err = %v, want ErrMediaGone", err)
	}
}

func TestStreamMediaRemovesTheTempDirOnDownloadError(t *testing.T) {
	var dir string
	withDownload(t, func(_ context.Context, _ *whatsmeow.Client, _ whatsmeow.DownloadableMessage, f *os.File) error {
		dir = dirOf(f.Name())
		return errors.New("socket closed")
	})
	svc := serviceMessage{chatStorageRepo: mediaStreamRepo{msg: storedImage}}
	if _, err := svc.StreamMedia(mediaStreamCtx(), "IMG1"); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %s still exists", dir)
	}
}

func TestStreamMediaStreamsAndCleansUp(t *testing.T) {
	var dir string
	withDownload(t, func(_ context.Context, _ *whatsmeow.Client, _ whatsmeow.DownloadableMessage, f *os.File) error {
		dir = dirOf(f.Name())
		_, err := f.Write([]byte("\x89PNG\r\n\x1a\nimage-bytes"))
		return err
	})
	svc := serviceMessage{chatStorageRepo: mediaStreamRepo{msg: storedImage}}
	stream, err := svc.StreamMedia(mediaStreamCtx(), "IMG1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(stream.File)
	if string(body) != "\x89PNG\r\n\x1a\nimage-bytes" || stream.Size != int64(len(body)) || stream.Mime != "image/png" {
		t.Fatalf("stream = %q size=%d mime=%s", body, stream.Size, stream.Mime)
	}
	if err := stream.File.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %s still exists after Close", dir)
	}
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == os.PathSeparator {
			return path[:i]
		}
	}
	return path
}
