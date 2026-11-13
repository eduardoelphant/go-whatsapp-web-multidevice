package usecase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/disintegration/imaging"
	fiberUtils "github.com/gofiber/utils/v2"
)

// errStickerToolMissing says neither ffmpeg nor cwebp is installed.
var errStickerToolMissing = errors.New("neither ffmpeg nor cwebp is installed for WebP conversion")

// stickerConvertError is a failure of the PNG save, the WebP command or the WebP read.
// msg is the text each route shows; err and stderr keep the cause.
type stickerConvertError struct {
	err    error
	stderr string
	msg    string
}

func (e stickerConvertError) Error() string { return e.msg }
func (e stickerConvertError) Unwrap() error { return e.err }

// stickerErrorMessage is the text both routes show for an error of encodeStillStickerWebP.
func stickerErrorMessage(err error) string {
	return err.Error()
}

// stickerErrorToAttendance maps the helper error to the 500 /send/sticker answers.
func stickerErrorToAttendance(err error) error {
	return pkgError.InternalServerError(stickerErrorMessage(err))
}

// stickerErrorToChannel maps the helper error to the 422 MEDIA_BUILD_FAILED of channels.
func stickerErrorToChannel(err error) error {
	return pkgError.MediaBuildFailed(stickerErrorMessage(err))
}

// voiceNoteOpusFFmpegArgs is the recipe for WhatsApp voice notes, shared by
// SendAudio and the channel audio: Opus, 64 kbps VBR tuned for voice, 48 kHz mono.
func voiceNoteOpusFFmpegArgs(inputPath, outputPath string) []string {
	return []string{
		"-i", inputPath,
		"-c:a", "libopus",
		"-b:a", "64k",
		"-vbr", "on",
		"-application", "voip",
		"-ar", "48000",
		"-ac", "1",
		"-y",
		outputPath,
	}
}

// encodeStillStickerWebP fits img in the sticker box, converts it to a static WebP
// (ffmpeg, else cwebp) inside dir and removes its temporary files. It returns the
// bytes and the fitted dimensions.
func encodeStillStickerWebP(ctx context.Context, img image.Image, dir, prefix string) ([]byte, uint32, uint32, error) {
	fitted := utils.FitBox(img, stickerMaxEdge, stickerMaxEdge)

	id := fiberUtils.UUIDv4()
	pngPath := filepath.Join(dir, fmt.Sprintf("%s_%s.png", prefix, id))
	webpPath := filepath.Join(dir, fmt.Sprintf("%s_%s.webp", prefix, id))
	defer os.Remove(pngPath)
	defer os.Remove(webpPath)

	if err := imaging.Save(fitted, pngPath); err != nil {
		return nil, 0, 0, stickerConvertError{err: err, msg: fmt.Sprintf("failed to save temporary PNG: %v", err)}
	}

	convCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var command *exec.Cmd
	if _, lookErr := exec.LookPath("ffmpeg"); lookErr == nil {
		command = exec.CommandContext(convCtx, "ffmpeg", buildStickerWebPFFmpegArgs(pngPath, webpPath)...)
	} else if _, lookErr := exec.LookPath("cwebp"); lookErr == nil {
		command = exec.CommandContext(convCtx, "cwebp", "-q", "60", "-o", webpPath, pngPath)
	} else {
		return nil, 0, 0, errStickerToolMissing
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, 0, 0, stickerConvertError{err: err, stderr: stderr.String(), msg: fmt.Sprintf("failed to convert sticker to WebP: %v, stderr: %s", err, stderr.String())}
	}

	webp, err := os.ReadFile(webpPath)
	if err != nil {
		return nil, 0, 0, stickerConvertError{err: err, msg: fmt.Sprintf("failed to read WebP sticker: %v", err)}
	}
	bounds := fitted.Bounds()
	return webp, uint32(bounds.Dx()), uint32(bounds.Dy()), nil
}
