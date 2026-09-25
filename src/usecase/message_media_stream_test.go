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
	msg      *domainChatStorage.Message
	deviceID *string
}

func (r mediaStreamRepo) GetMessageByIDAndDevice(deviceID, _ string) (*domainChatStorage.Message, error) {
	if r.deviceID != nil {
		*r.deviceID = deviceID
	}
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
	// whatsmeow stops retrying on 403, 404 and 410: all mean the CDN no longer has the file.
	for name, cdnErr := range map[string]error{
		"403": whatsmeow.ErrMediaDownloadFailedWith403,
		"404": whatsmeow.ErrMediaDownloadFailedWith404,
		"410": whatsmeow.ErrMediaDownloadFailedWith410,
	} {
		t.Run(name, func(t *testing.T) {
			withDownload(t, func(context.Context, *whatsmeow.Client, whatsmeow.DownloadableMessage, *os.File) error {
				return cdnErr
			})
			svc := serviceMessage{chatStorageRepo: mediaStreamRepo{msg: storedImage}}
			if _, err := svc.StreamMedia(mediaStreamCtx(), "IMG1"); !errors.Is(err, domainMessage.ErrMediaGone) {
				t.Fatalf("err = %v, want ErrMediaGone", err)
			}
		})
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

func TestStreamMediaScopesTheLookupToTheDeviceStorageID(t *testing.T) {
	withDownload(t, func(_ context.Context, _ *whatsmeow.Client, _ whatsmeow.DownloadableMessage, f *os.File) error {
		_, err := f.Write([]byte("x"))
		return err
	})
	var got string
	svc := serviceMessage{chatStorageRepo: mediaStreamRepo{msg: storedImage, deviceID: &got}}
	stream, err := svc.StreamMedia(mediaStreamCtx(), "IMG1")
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.File.Close()
	if got != "5511900000009@s.whatsapp.net" {
		t.Fatalf("device id = %q, want the store JID without device suffix", got)
	}
}

func TestStreamMediaDownloadOutlivesTheRequestDeadline(t *testing.T) {
	withDownload(t, func(ctx context.Context, _ *whatsmeow.Client, _ whatsmeow.DownloadableMessage, f *os.File) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, err := f.Write([]byte("x"))
		return err
	})
	ctx, cancel := context.WithCancel(mediaStreamCtx())
	cancel() // the 45s request deadline already fired
	svc := serviceMessage{chatStorageRepo: mediaStreamRepo{msg: storedImage}}
	stream, err := svc.StreamMedia(ctx, "IMG1")
	if err != nil {
		t.Fatalf("err = %v, want the download to use its own deadline", err)
	}
	_ = stream.File.Close()
}

func TestMediaMime(t *testing.T) {
	cases := []struct{ sniffed, mediaType, filename, want string }{
		{"image/jpeg", "image", "", "image/jpeg"},
		{"application/ogg", "audio", "", "audio/ogg"},
		{"application/zip", "document", "report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"application/octet-stream", "document", "data.csv", "text/csv; charset=utf-8"},
		{"application/octet-stream", "document", "noext", "application/octet-stream"},
		{"application/pdf", "document", "a.pdf", "application/pdf"},
	}
	for _, tc := range cases {
		if got := mediaMime(tc.sniffed, tc.mediaType, tc.filename); got != tc.want {
			t.Errorf("mediaMime(%q, %q, %q) = %q, want %q", tc.sniffed, tc.mediaType, tc.filename, got, tc.want)
		}
	}
}
