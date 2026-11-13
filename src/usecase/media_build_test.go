package usecase

import (
	"context"
	"errors"
	"image"
	"image/color"
	"os"
	"os/exec"
	"testing"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVoiceNoteOpusFFmpegArgs(t *testing.T) {
	assert.Equal(t, []string{
		"-i", "in.mp3",
		"-c:a", "libopus",
		"-b:a", "64k",
		"-vbr", "on",
		"-application", "voip",
		"-ar", "48000",
		"-ac", "1",
		"-y",
		"out.ogg",
	}, voiceNoteOpusFFmpegArgs("in.mp3", "out.ogg"))
}

func TestEncodeStillStickerWebP(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if _, err2 := exec.LookPath("cwebp"); err2 != nil {
			t.Skip("neither ffmpeg nor cwebp installed")
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, 1024, 256))
	for x := 0; x < 1024; x++ {
		for y := 0; y < 256; y++ {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	dir := t.TempDir()
	data, w, h, err := encodeStillStickerWebP(context.Background(), img, dir, "sticker")
	require.NoError(t, err)
	assert.Equal(t, uint32(512), w)
	assert.Equal(t, uint32(128), h)
	require.GreaterOrEqual(t, len(data), 12)
	assert.Equal(t, "RIFF", string(data[:4]))
	assert.Equal(t, "WEBP", string(data[8:12]))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestStickerErrorTranslation(t *testing.T) {
	missing := errStickerToolMissing
	failed := stickerConvertError{err: errors.New("boom"), stderr: "bad", msg: "failed to convert sticker to WebP: boom, stderr: bad"}

	attendance := stickerErrorToAttendance(missing)
	assert.Equal(t, pkgError.InternalServerError("neither ffmpeg nor cwebp is installed for WebP conversion"), attendance)
	assert.Equal(t, pkgError.MediaBuildFailed("neither ffmpeg nor cwebp is installed for WebP conversion"), stickerErrorToChannel(missing))

	assert.Equal(t, pkgError.InternalServerError("failed to convert sticker to WebP: boom, stderr: bad"), stickerErrorToAttendance(failed))
	assert.Equal(t, pkgError.MediaBuildFailed("failed to convert sticker to WebP: boom, stderr: bad"), stickerErrorToChannel(failed))
}
