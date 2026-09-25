package usecase

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"go.mau.fi/whatsmeow"
)

// Fork (elphant): streams a message's media to the caller through a temp
// file that is deleted when the stream is closed; nothing lands in /statics.

// mediaStreamDownloadTimeout replaces the global 45s request deadline for the
// download itself, so large media does not fail halfway.
const mediaStreamDownloadTimeout = 10 * time.Minute

var mediaStreamDownloadFn = func(ctx context.Context, client *whatsmeow.Client, msg whatsmeow.DownloadableMessage, file *os.File) error {
	return client.DownloadToFile(ctx, msg, file)
}

type tempMediaFile struct {
	*os.File
	dir string
}

func (f *tempMediaFile) Close() error {
	err := f.File.Close()
	_ = os.RemoveAll(f.dir)
	return err
}

func (service serviceMessage) StreamMedia(ctx context.Context, messageID string) (domainMessage.MediaStream, error) {
	client := whatsapp.ClientFromContext(ctx)
	if client == nil || client.Store == nil || client.Store.ID == nil {
		return domainMessage.MediaStream{}, pkgError.ErrWaCLI
	}
	stored, err := service.chatStorageRepo.GetMessageByIDAndDevice(client.Store.ID.ToNonAD().String(), messageID)
	if err != nil {
		return domainMessage.MediaStream{}, fmt.Errorf("look up message %s: %w", messageID, err)
	}
	if stored == nil || stored.MediaType == "" || utils.ResolveMediaDirectPath(stored.DirectPath, stored.URL) == "" {
		return domainMessage.MediaStream{}, domainMessage.ErrMediaNotFound
	}
	downloadable, err := utils.BuildDownloadableMessage(stored.MediaType, stored.URL, stored.DirectPath, stored.Filename,
		stored.MediaKey, stored.FileSHA256, stored.FileEncSHA256, stored.FileLength)
	if err != nil {
		return domainMessage.MediaStream{}, domainMessage.ErrMediaNotFound
	}

	dir, err := os.MkdirTemp("", "gowa-media-*")
	if err != nil {
		return domainMessage.MediaStream{}, err
	}
	file, err := os.Create(filepath.Join(dir, "media"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return domainMessage.MediaStream{}, err
	}
	fail := func(err error) (domainMessage.MediaStream, error) {
		_ = file.Close()
		_ = os.RemoveAll(dir)
		return domainMessage.MediaStream{}, err
	}
	dlCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mediaStreamDownloadTimeout)
	defer cancel()
	if err := mediaStreamDownloadFn(dlCtx, client, downloadable, file); err != nil {
		// whatsmeow stops retrying on 403, 404 and 410: the CDN no longer has the file.
		if errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) ||
			errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410) {
			return fail(domainMessage.ErrMediaGone)
		}
		return fail(fmt.Errorf("download media %s: %w", messageID, err))
	}
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	head := make([]byte, 512)
	n, _ := file.ReadAt(head, 0)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return domainMessage.MediaStream{
		File:     &tempMediaFile{File: file, dir: dir},
		Size:     info.Size(),
		Mime:     mediaMime(http.DetectContentType(head[:n]), stored.MediaType, stored.Filename),
		Filename: stored.Filename,
	}, nil
}

// mediaMime refines the sniffed Content-Type: sniffing cannot tell a docx from
// a zip or a voice note from generic Ogg, so generic results fall back to the
// file extension, and Ogg audio is reported as audio/ogg. stable.media.mime
// from the webhook stays the authoritative value.
func mediaMime(sniffed, mediaType, filename string) string {
	generic := sniffed == "application/octet-stream" || sniffed == "application/zip" ||
		sniffed == "application/ogg" || strings.HasPrefix(sniffed, "text/plain")
	if !generic {
		return sniffed
	}
	if ext := filepath.Ext(filename); ext != "" {
		if byExt := mime.TypeByExtension(strings.ToLower(ext)); byExt != "" {
			return byExt
		}
	}
	if mediaType == "audio" && sniffed == "application/ogg" {
		return "audio/ogg"
	}
	return sniffed
}
