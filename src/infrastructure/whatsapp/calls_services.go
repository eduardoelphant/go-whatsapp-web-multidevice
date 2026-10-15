package whatsapp

import (
	"context"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrtc"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/purpshell/meowcaller/opus"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): what voice calls need besides the clients (stage 2b).

// callsMedia is the browser-leg factory every call manager uses; nil until StartCallsServices
// opens the WebRTC port (calls then report ErrCallsNoMedia).
var callsMedia mediaFactory

// StartCallsServices runs once at start, before the first client is created. Retention runs even
// with calls off, so recordings of earlier calls still expire; the WebRTC port opens only with
// calls on.
func StartCallsServices(ctx context.Context) {
	retention := time.Duration(config.WhatsappCallsRecordingRetentionDays) * 24 * time.Hour
	callrec.StartRetention(ctx, config.PathCalls, retention)
	if !config.WhatsappCallsEnabled {
		return
	}
	if !opus.Available {
		logrus.Error("[CALLS] calls need libopus (a cgo build); browser audio and recordings are off")
		return
	}
	eng, err := callrtc.NewEngine(callrtc.Config{Port: config.WhatsappCallsRTCPort, PublicIP: config.WhatsappCallsRTCPublicIP})
	if err != nil {
		logrus.Errorf("[CALLS] %v; calls cannot carry browser audio", err)
		return
	}
	if config.WhatsappCallsRTCPublicIP == "" {
		logrus.Warn("[CALLS] WHATSAPP_CALLS_RTC_PUBLIC_IP is empty: the SDP carries local addresses, reachable only from the same network")
	}
	callsMedia = newBridgeFactory(pionEngine{eng})
	safego.Go("calls_rtc_close", func() {
		<-ctx.Done()
		_ = eng.Close()
	})
	logrus.Infof("[CALLS] WebRTC on UDP %d", config.WhatsappCallsRTCPort)
}

func newBridgeFactory(rtc rtcEngine) *bridgeFactory {
	return &bridgeFactory{
		rtc:       rtc,
		recordDir: config.PathCalls,
		grace:     time.Duration(config.WhatsappCallsRTCGraceSeconds) * time.Second,
		newRecorder: func(path string) (recorder, error) {
			r, err := callrec.Create(path)
			if err != nil {
				return nil, err
			}
			return r, nil
		},
	}
}
