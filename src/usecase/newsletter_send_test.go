package usecase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/build"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

type fakeUpload struct {
	size      int
	mediaType whatsmeow.MediaType
	data      []byte
}

type fakePollCall struct {
	name    string
	options []string
	max     int
}

func (f *fakeNewsletterAPI) UploadNewsletter(_ context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	f.calls = append(f.calls, "upload")
	f.uploads = append(f.uploads, fakeUpload{size: len(data), mediaType: mediaType, data: data})
	if f.uploadErr != nil {
		return whatsmeow.UploadResponse{}, f.uploadErr
	}
	return whatsmeow.UploadResponse{
		URL: "https://mmg.whatsapp.net/x", DirectPath: "/v/x", Handle: "HANDLE-" + f.handleSeed,
		FileSHA256: []byte{1, 2}, FileLength: uint64(len(data)),
	}, nil
}

func (f *fakeNewsletterAPI) SendMessage(_ context.Context, to types.JID, message *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	f.calls = append(f.calls, "send")
	f.sendTo = to
	f.sendMsg = message
	if len(extra) > 0 {
		f.sendExtra = extra[0]
	}
	if f.sendErr != nil {
		return whatsmeow.SendResponse{}, f.sendErr
	}
	resp := f.sendResp
	if resp.ID == "" {
		resp.ID = f.sendExtra.ID
	}
	if resp.ID == "" {
		resp.ID = "3EB0GENERATED"
	}
	return resp, nil
}

func (f *fakeNewsletterAPI) BuildPollCreation(name string, options []string, max int) *waE2E.Message {
	f.pollCalls = append(f.pollCalls, fakePollCall{name, options, max})
	return &waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{Name: &name}}
}

func (f *fakeNewsletterAPI) BuildEdit(chat types.JID, id types.MessageID, content *waE2E.Message) *waE2E.Message {
	f.calls = append(f.calls, "build_edit")
	f.editChat, f.editID, f.editContent = chat, id, content
	return &waE2E.Message{EditedMessage: &waE2E.FutureProofMessage{Message: content}}
}

func (f *fakeNewsletterAPI) BuildRevoke(chat, sender types.JID, id types.MessageID) *waE2E.Message {
	f.calls = append(f.calls, "build_revoke")
	f.revokeChat, f.revokeSender, f.revokeID = chat, sender, id
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{}}
}

const sendTestJID = "120363000000000001@newsletter"

func useTempSendItems(t *testing.T) {
	t.Helper()
	previous := config.PathSendItems
	config.PathSendItems = t.TempDir()
	t.Cleanup(func() { config.PathSendItems = previous })
}

func newSendService(api *fakeNewsletterAPI) serviceNewsletter {
	service := newTestNewsletterService(api)
	service.linkPreview = func(context.Context, newsletterAPI, types.JID, string, string) (*waE2E.Message, error) {
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Title: stringPointer("Prévia")}}, nil
	}
	return service
}

func tinyJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var buffer bytes.Buffer
	require.NoError(t, jpeg.Encode(&buffer, img, nil))
	return buffer.Bytes()
}

func TestNewsletterSendMessageIDInformadoViraExtraID(t *testing.T) {
	api := &fakeNewsletterAPI{sendResp: whatsmeow.SendResponse{ServerID: 777, Timestamp: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}}
	response, err := newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
		NewsletterID: sendTestJID, MessageID: "3EB0AAAABBBBCCCCDDDD", Type: "text", Text: "Olá",
	})
	require.NoError(t, err)
	assert.Equal(t, types.MessageID("3EB0AAAABBBBCCCCDDDD"), api.sendExtra.ID)
	assert.Equal(t, "3EB0AAAABBBBCCCCDDDD", response.MessageID)
	assert.Equal(t, 777, response.ServerID)
	assert.Equal(t, "2026-11-12T10:00:00Z", response.Timestamp)
	assert.Equal(t, "Olá", api.sendMsg.GetConversation())
	assert.Equal(t, types.NewsletterServer, api.sendTo.Server)
	assert.False(t, api.sendExtra.CheckPermissions, "canal não passa por CheckPermissions")
}

func TestNewsletterSendSemMessageIDDeixaOForkGerar(t *testing.T) {
	api := &fakeNewsletterAPI{}
	response, err := newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
		NewsletterID: sendTestJID, Type: "text", Text: "Olá",
	})
	require.NoError(t, err)
	assert.Empty(t, api.sendExtra.ID)
	assert.Equal(t, "3EB0GENERATED", response.MessageID)
}

func TestNewsletterSendMidiaSobePorUploadNewsletterEHandleVaiNoExtra(t *testing.T) {
	cases := []struct {
		name      string
		request   domainNewsletter.SendRequest
		mediaType whatsmeow.MediaType
		check     func(t *testing.T, msg *waE2E.Message)
	}{
		{
			name:      "image",
			request:   domainNewsletter.SendRequest{Type: "image", Text: "legenda", File: tinyJPEG(t), FileName: "a.jpg"},
			mediaType: whatsmeow.MediaImage,
			check: func(t *testing.T, msg *waE2E.Message) {
				assert.Equal(t, "legenda", msg.GetImageMessage().GetCaption())
				assert.Equal(t, uint32(8), msg.GetImageMessage().GetWidth())
				assert.Equal(t, uint32(6), msg.GetImageMessage().GetHeight())
				assert.Equal(t, "/v/x", msg.GetImageMessage().GetDirectPath())
			},
		},
		{
			name:      "video",
			request:   domainNewsletter.SendRequest{Type: "video", Text: "v", FileName: "a.mp4"},
			mediaType: whatsmeow.MediaVideo,
			check: func(t *testing.T, msg *waE2E.Message) {
				assert.Equal(t, "v", msg.GetVideoMessage().GetCaption())
				assert.Equal(t, "video/mp4", msg.GetVideoMessage().GetMimetype())
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.request.Type == "video" {
				tc.request.File = realTestMP4(t)
			}
			api := &fakeNewsletterAPI{handleSeed: tc.name}
			tc.request.NewsletterID = sendTestJID
			_, err := newSendService(api).Send(context.Background(), tc.request)
			require.NoError(t, err)
			require.Len(t, api.uploads, 1)
			assert.Equal(t, tc.mediaType, api.uploads[0].mediaType)
			assert.Equal(t, "HANDLE-"+tc.name, api.sendExtra.MediaHandle, "o handle do upload tem de ir em SendRequestExtra.MediaHandle")
			assert.Equal(t, []string{"upload", "send"}, api.calls)
			tc.check(t, api.sendMsg)
		})
	}
}

func TestNewsletterSendAudioOggEMp3SaemComoVoz(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg ausente")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe ausente")
	}
	useTempSendItems(t)
	cases := []struct {
		name     string
		filename string
		ext      string
	}{
		{"ogg", "voz.ogg", "ogg"},
		{"ogg sem extensao", "voz", "ogg"},
		{"mp3", "musica.mp3", "mp3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srcPath := filepath.Join(t.TempDir(), "tone."+tc.ext)
			out, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=duration=2", srcPath).CombinedOutput()
			require.NoError(t, err, string(out))
			data, err := os.ReadFile(srcPath)
			require.NoError(t, err)

			api := &fakeNewsletterAPI{}
			_, err = newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
				NewsletterID: sendTestJID, Type: "audio", File: data, FileName: tc.filename,
			})
			require.NoError(t, err)
			audio := api.sendMsg.GetAudioMessage()
			assert.True(t, audio.GetPTT())
			assert.Equal(t, "audio/ogg; codecs=opus", audio.GetMimetype())
			assert.NotEmpty(t, audio.GetWaveform())
			assert.Greater(t, audio.GetSeconds(), uint32(0))
			assert.Equal(t, whatsmeow.MediaAudio, api.uploads[0].mediaType)
			assert.NotEmpty(t, api.sendExtra.MediaHandle)
		})
	}
}

func TestNewsletterSendFigurinhaConvertePraWebP(t *testing.T) {
	useTempSendItems(t)
	api := &fakeNewsletterAPI{}
	_, err := newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
		NewsletterID: sendTestJID, Type: "sticker", File: tinyJPEG(t), FileName: "s.jpg",
	})
	if err != nil && (bytes.Contains([]byte(err.Error()), []byte("neither ffmpeg nor cwebp"))) {
		t.Skip("ffmpeg e cwebp ausentes")
	}
	require.NoError(t, err)
	require.Len(t, api.uploads, 1)
	assert.Equal(t, whatsmeow.MediaImage, api.uploads[0].mediaType, "figurinha sobe como MediaImage, igual ao /send/sticker")
	sticker := api.sendMsg.GetStickerMessage()
	require.NotNil(t, sticker)
	assert.Equal(t, "image/webp", sticker.GetMimetype())
	assert.Equal(t, "HANDLE-", api.sendExtra.MediaHandle)
	assert.Equal(t, uint32(8), sticker.GetWidth())
}

func TestNewsletterSendEnqueteMontaBuildPollCreation(t *testing.T) {
	cases := []struct {
		name    string
		max     int
		wantMax int
	}{
		{"ausente vira 1", 0, 1},
		{"informado", 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeNewsletterAPI{}
			_, err := newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
				NewsletterID: sendTestJID, Type: "poll", PollQuestion: "Qual?",
				PollOptions: []string{"A", "B", "A"}, PollMaxAnswer: tc.max,
			})
			require.NoError(t, err)
			require.Len(t, api.pollCalls, 1)
			assert.Equal(t, "Qual?", api.pollCalls[0].name)
			assert.Equal(t, []string{"A", "B", "A"}, api.pollCalls[0].options, "opções repetidas seguem")
			assert.Equal(t, tc.wantMax, api.pollCalls[0].max)
			assert.Empty(t, api.uploads)
			assert.Empty(t, api.sendExtra.MediaHandle)
		})
	}
}

func TestNewsletterSendTextoComPrevia(t *testing.T) {
	api := &fakeNewsletterAPI{}
	_, err := newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
		NewsletterID: sendTestJID, Type: "text", Text: "Veja https://exemplo.com/x agora", LinkPreview: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Prévia", api.sendMsg.GetExtendedTextMessage().GetTitle())
}

func TestNewsletterSendPreviaFalhaEnviaTextoPuro(t *testing.T) {
	api := &fakeNewsletterAPI{}
	service := newSendService(api)
	service.linkPreview = func(context.Context, newsletterAPI, types.JID, string, string) (*waE2E.Message, error) {
		return nil, errors.New("timeout")
	}
	_, err := service.Send(context.Background(), domainNewsletter.SendRequest{
		NewsletterID: sendTestJID, Type: "text", Text: "Veja https://exemplo.com/x", LinkPreview: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Veja https://exemplo.com/x", api.sendMsg.GetConversation())
	assert.Nil(t, api.sendMsg.GetExtendedTextMessage())
}

func TestNewsletterSendPreviaSoComLinkPreviewLigado(t *testing.T) {
	api := &fakeNewsletterAPI{}
	_, err := newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
		NewsletterID: sendTestJID, Type: "text", Text: "Veja https://exemplo.com/x",
	})
	require.NoError(t, err)
	assert.Equal(t, "Veja https://exemplo.com/x", api.sendMsg.GetConversation())
}

func TestNewsletterSendMapeiaErrosDoWhatsApp(t *testing.T) {
	cases := []struct {
		name       string
		sendErr    error
		uploadErr  error
		request    domainNewsletter.SendRequest
		wantStatus int
		wantCode   string
	}{
		{"not-authorized", whatsmeow.ErrIQNotAuthorized, nil, domainNewsletter.SendRequest{Type: "text", Text: "x"}, 403, "NOT_AUTHORIZED"},
		{"not-authorized embrulhado", errors.New("failed to send message node: info query returned status 401: not-authorized"), nil, domainNewsletter.SendRequest{Type: "text", Text: "x"}, 403, "NOT_AUTHORIZED"},
		{"item-not-found", whatsmeow.ErrIQNotFound, nil, domainNewsletter.SendRequest{Type: "text", Text: "x"}, 404, "NOT_FOUND"},
		{"midia recusada no envio", whatsmeow.ErrIQBadRequest, nil, domainNewsletter.SendRequest{Type: "video"}, 400, "MEDIA_REJECTED"},
		{"midia recusada no upload", nil, errors.New("upload failed with status code 413"), domainNewsletter.SendRequest{Type: "video"}, 400, "MEDIA_REJECTED"},
		{"recusa do servidor", fmt.Errorf("%w %d", whatsmeow.ErrServerReturnedError, 479), nil, domainNewsletter.SendRequest{Type: "text", Text: "x"}, 422, "SEND_REFUSED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.request.Type == "video" {
				tc.request.File = realTestMP4(t)
			}
			api := &fakeNewsletterAPI{sendErr: tc.sendErr, uploadErr: tc.uploadErr}
			tc.request.NewsletterID = sendTestJID
			_, err := newSendService(api).Send(context.Background(), tc.request)
			var generic pkgError.GenericError
			require.True(t, errors.As(err, &generic), "got %v", err)
			assert.Equal(t, tc.wantStatus, generic.StatusCode())
			assert.Equal(t, tc.wantCode, generic.ErrCode())
		})
	}
}

func TestNewsletterSendBadRequestSemMidiaNaoViraMediaRejected(t *testing.T) {
	api := &fakeNewsletterAPI{sendErr: whatsmeow.ErrIQBadRequest}
	_, err := newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "text", Text: "x"})
	require.Error(t, err)
	assert.ErrorIs(t, err, whatsmeow.ErrIQBadRequest)
}

func TestNewsletterSendInvalidoNaoChamaOCliente(t *testing.T) {
	tooMany := make([]string, 13)
	for i := range tooMany {
		tooMany[i] = "o"
	}
	cases := []struct {
		name    string
		request domainNewsletter.SendRequest
	}{
		{"sem id", domainNewsletter.SendRequest{Type: "text", Text: "x"}},
		{"id sem sufixo", domainNewsletter.SendRequest{NewsletterID: "123@g.us", Type: "text", Text: "x"}},
		{"tipo fora da lista", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "location", Text: "x"}},
		{"sem tipo", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Text: "x"}},
		{"texto vazio", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "text"}},
		{"imagem sem arquivo", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "image"}},
		{"video sem arquivo", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "video"}},
		{"audio sem arquivo", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "audio"}},
		{"figurinha sem arquivo", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "sticker"}},
		{"imagem que nao e imagem", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "image", File: []byte("nada")}},
		{"enquete sem pergunta", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "poll", PollOptions: []string{"a", "b"}}},
		{"enquete com 1 opcao", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "poll", PollQuestion: "q", PollOptions: []string{"a"}}},
		{"enquete com 13 opcoes", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "poll", PollQuestion: "q", PollOptions: tooMany}},
		{"enquete com opcao vazia", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "poll", PollQuestion: "q", PollOptions: []string{"a", ""}}},
		{"enquete com max maior que opcoes", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "poll", PollQuestion: "q", PollOptions: []string{"a", "b"}, PollMaxAnswer: 3}},
		{"enquete com max negativo", domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "poll", PollQuestion: "q", PollOptions: []string{"a", "b"}, PollMaxAnswer: -1}},
		{"message_id com espaco", domainNewsletter.SendRequest{NewsletterID: sendTestJID, MessageID: "3EB0 X", Type: "text", Text: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeNewsletterAPI{}
			_, err := newSendService(api).Send(context.Background(), tc.request)
			var generic pkgError.GenericError
			require.True(t, errors.As(err, &generic), "got %v", err)
			assert.Equal(t, 400, generic.StatusCode())
			assert.Empty(t, api.calls)
		})
	}
}

func TestNewsletterSendSemClienteEErroDeCLI(t *testing.T) {
	service := serviceNewsletter{apiFromContext: func(context.Context) newsletterAPI { return nil }}
	_, err := service.Send(context.Background(), domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "text", Text: "x"})
	assert.ErrorIs(t, err, pkgError.ErrWaCLI)
}

func TestNewsletterSendGravaNoChatstorageSemBloquear(t *testing.T) {
	api := &fakeNewsletterAPI{}
	service := newSendService(api)
	var stored []string
	service.storeSent = func(_ context.Context, _ newsletterAPI, id string, recipient types.JID, content string, _ time.Time, _ *waE2E.Message) {
		stored = append(stored, id+"|"+recipient.String()+"|"+content)
	}
	_, err := service.Send(context.Background(), domainNewsletter.SendRequest{NewsletterID: sendTestJID, MessageID: "3EB0ABC", Type: "text", Text: "Olá"})
	require.NoError(t, err)
	assert.Equal(t, []string{"3EB0ABC|" + sendTestJID + "|Olá"}, stored)
}

// realTestMP4 generates a tiny real mp4 (video send now transcodes with ffmpeg).
func realTestMP4(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg ausente")
	}
	useTempSendItems(t)
	path := filepath.Join(t.TempDir(), "r.mp4")
	out, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5", "-pix_fmt", "yuv420p", path).CombinedOutput()
	require.NoError(t, err, string(out))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func TestNewsletterSendVideoComPreviaEDuracao(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg ausente")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe ausente")
	}
	useTempSendItems(t)
	path := filepath.Join(t.TempDir(), "v.mp4")
	out, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "testsrc=duration=3:size=160x120:rate=10", "-pix_fmt", "yuv420p", path).CombinedOutput()
	require.NoError(t, err, string(out))
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	api := &fakeNewsletterAPI{}
	_, err = newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
		NewsletterID: sendTestJID, Type: "video", File: data, FileName: "v.mp4",
	})
	require.NoError(t, err)
	video := api.sendMsg.GetVideoMessage()
	assert.NotEmpty(t, video.GetJPEGThumbnail())
	assert.Greater(t, video.GetSeconds(), uint32(0))
	assert.Equal(t, uint32(160), video.GetWidth())
	assert.Empty(t, video.GetThumbnailDirectPath())
	assert.Empty(t, video.GetThumbnailSHA256())
}

func TestNewsletterSendVideoInvalidoDevolveMediaBuildFailed(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg ausente")
	}
	useTempSendItems(t)
	api := &fakeNewsletterAPI{}
	_, err := newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
		NewsletterID: sendTestJID, Type: "video", File: []byte("nao e video"), FileName: "v.mp4",
	})
	var generic pkgError.GenericError
	require.True(t, errors.As(err, &generic))
	assert.Equal(t, "MEDIA_BUILD_FAILED", generic.ErrCode())
	assert.Empty(t, api.calls)
}

func TestNewsletterSendVideoTranscodificaComFaststartEYuv420p(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg ausente")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe ausente")
	}
	useTempSendItems(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "v.mp4")
	out, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "testsrc=duration=2:size=160x120:rate=10", "-c:v", "libx264", "-pix_fmt", "yuv444p", path).CombinedOutput()
	require.NoError(t, err, string(out))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Less(t, bytes.Index(data, []byte("mdat")), bytes.Index(data, []byte("moov")), "entrada deve ser sem faststart")

	api := &fakeNewsletterAPI{}
	_, err = newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
		NewsletterID: sendTestJID, Type: "video", File: data, FileName: "v.mp4",
	})
	require.NoError(t, err)
	require.Len(t, api.uploads, 1)
	sent := api.uploads[0].data
	moov, mdat := bytes.Index(sent, []byte("moov")), bytes.Index(sent, []byte("mdat"))
	require.GreaterOrEqual(t, moov, 0)
	assert.Less(t, moov, mdat)
	outPath := filepath.Join(dir, "sent.mp4")
	require.NoError(t, os.WriteFile(outPath, sent, 0o644))
	probe, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=pix_fmt", "-of", "default=nw=1:nk=1", outPath).Output()
	require.NoError(t, err)
	assert.Equal(t, "yuv420p", strings.TrimSpace(string(probe)))
	assert.Equal(t, "video/mp4", api.sendMsg.GetVideoMessage().GetMimetype())
}

func TestNewsletterUploadErroSoRecusaEmStatusDeArquivo(t *testing.T) {
	for _, status := range []string{"400", "403", "413", "415", "422"} {
		err := mapNewsletterUploadError(fmt.Errorf("upload failed with status code %s", status))
		var generic pkgError.GenericError
		require.True(t, errors.As(err, &generic), status)
		assert.Equal(t, "MEDIA_REJECTED", generic.ErrCode())
	}
	for _, status := range []string{"429", "500", "503"} {
		raw := fmt.Errorf("upload failed with status code %s", status)
		assert.Equal(t, raw, mapNewsletterUploadError(raw), status)
	}
}

func TestNewsletterUploadTextoDoWhatsmeowContinuaOMesmo(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(build.Default.GOPATH, "pkg/mod/go.mau.fi/whatsmeow@v0.0.0-20261028111316-d24d079a3755/upload.go"))
	if err != nil {
		t.Skip("fonte do whatsmeow indisponível")
	}
	assert.Contains(t, string(src), `"`+newsletterUploadStatusText+` %d"`)
}

func TestNewsletterSendFigurinhaAnimadaRecusa(t *testing.T) {
	animated := append([]byte("RIFF\x40\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x12\x00\x00\x00\x00\x00\x00\x00\x00\x00ANIM"), make([]byte, 20)...)
	api := &fakeNewsletterAPI{}
	_, err := newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
		NewsletterID: sendTestJID, Type: "sticker", File: animated, FileName: "a.webp",
	})
	var generic pkgError.GenericError
	require.True(t, errors.As(err, &generic))
	assert.Equal(t, 400, generic.StatusCode())
	assert.Contains(t, err.Error(), "animated WebP")
	assert.Empty(t, api.calls)
}

func TestNewsletterSendRecusaDoServidorTrazOTexto(t *testing.T) {
	api := &fakeNewsletterAPI{sendErr: fmt.Errorf("%w %d", whatsmeow.ErrServerReturnedError, 479)}
	_, err := newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "text", Text: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server returned error 479")
}

func TestNewsletterServerReturnedErrorTextoDoWhatsmeowContinuaOMesmo(t *testing.T) {
	assert.Equal(t, "server returned error", whatsmeow.ErrServerReturnedError.Error())
	src, err := os.ReadFile(filepath.Join(build.Default.GOPATH, "pkg/mod/go.mau.fi/whatsmeow@v0.0.0-20261028111316-d24d079a3755/errors.go"))
	if err != nil {
		t.Skip("fonte do whatsmeow indisponível")
	}
	assert.Contains(t, string(src), `errors.New("server returned error")`)
}

func TestNewsletterSendDocumentoRecusadoComTypeUnsupportedSemChamarOCliente(t *testing.T) {
	api := &fakeNewsletterAPI{}
	_, err := newSendService(api).Send(context.Background(), domainNewsletter.SendRequest{
		NewsletterID: sendTestJID, Type: " Document ", Filename: "tabela.pdf", File: []byte("%PDF-1.4"),
	})
	var generic pkgError.GenericError
	require.True(t, errors.As(err, &generic), "got %v", err)
	assert.Equal(t, 422, generic.StatusCode())
	assert.Equal(t, "TYPE_UNSUPPORTED", generic.ErrCode())
	assert.Empty(t, api.calls)
}

func TestConvertNewsletterAudioToOpus(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg ausente")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe ausente")
	}
	previous := config.PathSendItems
	config.PathSendItems = t.TempDir()
	t.Cleanup(func() { config.PathSendItems = previous })

	t.Run("mp3 vira ogg opus", func(t *testing.T) {
		mp3Path := filepath.Join(t.TempDir(), "tone.mp3")
		out, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=duration=2", mp3Path).CombinedOutput()
		require.NoError(t, err, string(out))
		mp3, err := os.ReadFile(mp3Path)
		require.NoError(t, err)

		got, err := convertNewsletterAudioToOpus(context.Background(), mp3, "tone.mp3")
		require.NoError(t, err)
		assert.True(t, bytes.HasPrefix(got, []byte("OggS")))

		oggPath := filepath.Join(t.TempDir(), "out.ogg")
		require.NoError(t, os.WriteFile(oggPath, got, 0o644))
		probe, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "default=nw=1:nk=1", oggPath).CombinedOutput()
		require.NoError(t, err, string(probe))
		assert.Equal(t, "opus", strings.TrimSpace(string(probe)))

		left, _ := os.ReadDir(config.PathSendItems)
		assert.Empty(t, left)
	})

	t.Run("bytes invalidos devolvem MEDIA_BUILD_FAILED", func(t *testing.T) {
		_, err := convertNewsletterAudioToOpus(context.Background(), []byte("nao e audio"), "x.mp3")
		require.Error(t, err)
		var generic pkgError.GenericError
		require.True(t, errors.As(err, &generic))
		assert.Equal(t, "MEDIA_BUILD_FAILED", generic.ErrCode())
	})
}
