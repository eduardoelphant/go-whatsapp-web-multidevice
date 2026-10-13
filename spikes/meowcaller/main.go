// Command spike is a throwaway prototype that checks whether WhatsApp voice calls
// work on top of whatsmeow through meowcaller. See README.md.
//
//	spike pair                       Pair a new linked device (QR saved to data/qr.png).
//	spike answer [flags]             Auto-answer incoming calls.
//	spike call [flags] <target>      Place a call to target (+5527...).
//
// Flags for answer and call:
//
//	-play <file>     .wav/.mp3/.opus streamed to the peer once media is ready.
//	-max <duration>  Hang up this long after media is ready (0 = never).
//
// The peer's audio is always recorded to data/rec/<call-id>.wav.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	_ "modernc.org/sqlite"
)

const dataDir = "data"

func main() {
	level := zerolog.InfoLevel
	if lvl, err := zerolog.ParseLevel(os.Getenv("SPIKE_LOG_LEVEL")); err == nil && lvl != zerolog.NoLevel {
		level = lvl
	}
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05.000"}).
		Level(level).With().Timestamp().Logger()

	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithContext(ctx)

	if err := os.MkdirAll(filepath.Join(dataDir, "rec"), 0o700); err != nil {
		logger.Fatal().Err(err).Msg("create data dir")
	}

	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	play := fs.String("play", "", "audio file streamed to the peer once media is ready")
	maxDur := fs.Duration("max", 0, "hang up this long after media is ready (0 = never)")
	_ = fs.Parse(os.Args[2:])

	var err error
	switch os.Args[1] {
	case "pair":
		err = runPair(ctx)
	case "answer":
		err = runAnswer(ctx, *play, *maxDur)
	case "call":
		if fs.NArg() != 1 {
			usage()
		}
		err = runCall(ctx, fs.Arg(0), *play, *maxDur)
	default:
		usage()
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Fatal().Err(err).Str("command", os.Args[1]).Msg("command failed")
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: spike pair | answer [-play file] [-max 15m] | call [-play file] [-max 15m] <target>")
	os.Exit(2)
}

func runPair(ctx context.Context) error {
	wa, err := openClient(ctx)
	if err != nil {
		return err
	}
	if wa.Store.ID != nil {
		zerolog.Ctx(ctx).Info().Msg("already paired; delete data/session.db to pair again")
		return nil
	}
	if err := connect(ctx, wa); err != nil {
		return err
	}
	wa.Disconnect()
	return nil
}

func runAnswer(ctx context.Context, play string, maxDur time.Duration) error {
	log := zerolog.Ctx(ctx)
	wa, client, err := connectManaged(ctx)
	if err != nil {
		return err
	}
	defer wa.Disconnect()
	stopStats := startStats(ctx)
	defer stopStats()

	client.OnIncomingCall(func(call *meowcaller.Call) {
		t := newTimeline(ctx, call, "inbound")
		t.mark("offer")
		if err := call.Answer(); err != nil {
			log.Error().Err(err).Str("call_id", call.ID()).Msg("answer failed")
			return
		}
		t.mark("answered")
		wireCall(ctx, call, t, play, maxDur)
	})

	log.Info().Msg("waiting for incoming calls; Ctrl+C to stop")
	<-ctx.Done()
	return ctx.Err()
}

func runCall(ctx context.Context, target, play string, maxDur time.Duration) error {
	log := zerolog.Ctx(ctx)
	wa, client, err := connectManaged(ctx)
	if err != nil {
		return err
	}
	defer wa.Disconnect()
	stopStats := startStats(ctx)
	defer stopStats()

	start := time.Now()
	call, err := client.Call(ctx, target)
	if err != nil {
		return fmt.Errorf("place call: %w", err)
	}
	t := newTimeline(ctx, call, "outbound")
	t.start = start
	t.mark("offer_sent")
	call.OnPeerAccept(func() { t.mark("peer_accepted") })
	wireCall(ctx, call, t, play, maxDur)

	log.Info().Str("call_id", call.ID()).Msg("call placed; Ctrl+C to stop")
	select {
	case <-ctx.Done():
		_ = call.Hangup()
		return ctx.Err()
	case <-t.done:
		return nil
	}
}

// wireCall records the peer, plays the optional file, logs the lifecycle and
// hangs up after maxDur of media.
func wireCall(ctx context.Context, call *meowcaller.Call, t *timeline, play string, maxDur time.Duration) {
	log := zerolog.Ctx(ctx)
	recPath := filepath.Join(dataDir, "rec", call.ID()+".wav")
	if rec, err := meowcaller.WAVRecorder(recPath); err != nil {
		log.Error().Err(err).Msg("open recorder failed")
	} else {
		call.Receive(rec)
	}
	call.OnStateChange(func(p meowcaller.CallPhase) {
		log.Info().Str("call_id", call.ID()).Int("phase", int(p)).Msg("call state")
	})
	call.OnReady(func() {
		t.mark("media_ready")
		if play != "" {
			src, err := openSource(play)
			if err != nil {
				log.Error().Err(err).Str("file", play).Msg("open audio failed")
			} else {
				call.Play(src)
			}
		}
		if maxDur > 0 {
			time.AfterFunc(maxDur, func() {
				log.Info().Dur("after", maxDur).Msg("max duration reached; hanging up")
				_ = call.Hangup()
			})
		}
	})
	call.OnEnd(func(reason string) {
		t.mark("ended")
		t.finish(reason, recPath)
	})
}

func openSource(file string) (meowcaller.AudioSource, error) {
	switch ext := strings.ToLower(filepath.Ext(file)); ext {
	case ".mp3":
		return meowcaller.MP3File(file)
	case ".wav":
		return meowcaller.WAVFile(file)
	case ".opus":
		return meowcaller.OpusFile(file)
	default:
		return nil, fmt.Errorf("unsupported audio file extension %q", ext)
	}
}

// timeline records when each step of one call happened, relative to its start.
type timeline struct {
	log   zerolog.Logger
	start time.Time
	mu    sync.Mutex
	steps []string
	ready time.Time
	done  chan struct{}
	once  sync.Once
}

func newTimeline(ctx context.Context, call *meowcaller.Call, dir string) *timeline {
	return &timeline{
		log:   zerolog.Ctx(ctx).With().Str("call_id", call.ID()).Str("dir", dir).Str("peer", maskJID(call.Peer())).Logger(),
		start: time.Now(),
		done:  make(chan struct{}),
	}
}

func (t *timeline) mark(step string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	since := time.Since(t.start)
	if step == "media_ready" {
		t.ready = time.Now()
	}
	t.steps = append(t.steps, fmt.Sprintf("%s=%s", step, since.Round(time.Millisecond)))
	t.log.Info().Str("step", step).Dur("since_start", since).Msg("call step")
}

func (t *timeline) finish(reason, recPath string) {
	t.once.Do(func() {
		t.mu.Lock()
		var media time.Duration
		if !t.ready.IsZero() {
			media = time.Since(t.ready)
		}
		steps := strings.Join(t.steps, " ")
		t.mu.Unlock()
		t.log.Info().Str("reason", reason).Dur("media_duration", media).Str("steps", steps).
			Str("recording", recPath).Msg("call summary")
		close(t.done)
	})
}

// maskJID keeps only the last four digits of the peer's user part.
func maskJID(j types.JID) string {
	u := j.User
	if len(u) > 4 {
		u = "…" + u[len(u)-4:]
	}
	return u + "@" + j.Server
}

// startStats logs CPU, memory and network use of this process every 10 seconds.
func startStats(ctx context.Context) func() {
	log := zerolog.Ctx(ctx)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		prevCPU, prevAt := cpuTime(), time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			cpu, at := cpuTime(), time.Now()
			pct := float64(cpu-prevCPU) / float64(at.Sub(prevAt)) * 100
			prevCPU, prevAt = cpu, at
			in, out := netBytes()
			log.Info().Float64("cpu_pct", round1(pct)).Int64("rss_kb", rssKB()).
				Int64("net_in_bytes", in).Int64("net_out_bytes", out).Msg("stats")
		}
	}()
	return cancel
}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func rssKB() int64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", fmt.Sprint(os.Getpid())).Output()
	if err != nil {
		return -1
	}
	var kb int64
	_, _ = fmt.Sscan(strings.TrimSpace(string(out)), &kb)
	return kb
}

// netBytes returns this process's total bytes in and out, as reported by nettop.
func netBytes() (int64, int64) {
	out, err := exec.Command("nettop", "-P", "-L", "1", "-x", "-J", "bytes_in,bytes_out",
		"-p", fmt.Sprint(os.Getpid())).Output()
	if err != nil {
		return -1, -1
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return -1, -1
	}
	fields := strings.Split(lines[len(lines)-1], ",")
	if len(fields) < 3 {
		return -1, -1
	}
	var in, outB int64
	_, _ = fmt.Sscan(fields[1], &in)
	_, _ = fmt.Sscan(fields[2], &outB)
	return in, outB
}

func round1(f float64) float64 { return float64(int64(f*10)) / 10 }

func openClient(ctx context.Context) (*whatsmeow.Client, error) {
	store.DeviceProps.Os = proto.String("meowcaller spike")
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()

	waLogger := zerolog.Ctx(ctx).Level(zerolog.InfoLevel)
	dsn := "file:" + filepath.Join(dataDir, "session.db") + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	container, err := sqlstore.New(ctx, "sqlite", dsn, waLog.Zerolog(waLogger).Sub("db"))
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("load device: %w", err)
	}
	return whatsmeow.NewClient(device, waLog.Zerolog(waLogger).Sub("wa")), nil
}

// connectManaged installs meowcaller before connecting, so its call handlers
// see signaling from the first stanza.
func connectManaged(ctx context.Context) (*whatsmeow.Client, *meowcaller.Client, error) {
	wa, err := openClient(ctx)
	if err != nil {
		return nil, nil, err
	}
	if wa.Store.ID == nil {
		return nil, nil, errors.New("not paired; run `spike pair` first")
	}
	client := meowcaller.NewClient(wa, meowcaller.WithLogger(*zerolog.Ctx(ctx)))
	wa.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.LoggedOut:
			zerolog.Ctx(ctx).Error().Int("reason", int(e.Reason)).Msg("LOGGED OUT: stop and tell the owner")
		case *events.StreamReplaced:
			zerolog.Ctx(ctx).Error().Msg("STREAM REPLACED")
		case *events.TemporaryBan:
			zerolog.Ctx(ctx).Error().Str("ban", e.String()).Msg("TEMPORARY BAN: stop and tell the owner")
		case *events.Disconnected:
			zerolog.Ctx(ctx).Warn().Msg("disconnected")
		}
	})
	if err := connect(ctx, wa); err != nil {
		return nil, nil, err
	}
	return wa, client, nil
}

// connect logs in, writing the pairing QR to data/qr.png on first run. The QR
// content is never logged.
func connect(ctx context.Context, wa *whatsmeow.Client) error {
	log := zerolog.Ctx(ctx)
	if wa.Store.ID == nil {
		qr, _ := wa.GetQRChannel(ctx)
		if err := wa.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		qrPath := filepath.Join(dataDir, "qr.png")
		defer os.Remove(qrPath)
		opened := false
		for evt := range qr {
			if evt.Event != "code" {
				log.Info().Str("event", evt.Event).Msg("login event")
				continue
			}
			if err := qrcode.WriteFile(evt.Code, qrcode.Medium, 512, qrPath); err != nil {
				return fmt.Errorf("write qr: %w", err)
			}
			log.Info().Int("valid_s", int(evt.Timeout.Seconds())).Str("file", qrPath).
				Msg("scan in WhatsApp > Linked devices")
			if !opened {
				_ = exec.Command("open", qrPath).Run()
				opened = true
			}
		}
	} else if err := wa.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if err := waitUntilReady(ctx, wa, 60*time.Second); err != nil {
		return err
	}
	log.Info().Msg("connected")
	if wa.Store.PushName == "" {
		wa.Store.PushName = "meowcaller spike"
	}
	// Call signaling is only delivered to a client that announced availability.
	if err := wa.SendPresence(ctx, types.PresenceAvailable); err != nil {
		log.Warn().Err(err).Msg("send presence failed; continuing")
	}
	return nil
}

// waitUntilReady blocks until the client is connected and logged in, tolerating
// the expected disconnect and reconnect right after pairing.
func waitUntilReady(ctx context.Context, wa *whatsmeow.Client, timeout time.Duration) error {
	ready := make(chan struct{}, 8)
	id := wa.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.Connected); ok {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	})
	defer wa.RemoveEventHandler(id)
	deadline := time.After(timeout)
	for !(wa.IsConnected() && wa.IsLoggedIn()) {
		select {
		case <-ready:
		case <-deadline:
			return errors.New("timed out waiting for whatsmeow connection")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
