package cmd

import (
	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/spf13/viper"
)

// Fork (elphant): settings of voice calls stage 2b, kept out of root.go.
// See docs/specs/2026-10-14-gateway-voice-calls-2b-design.md.

func initCallsEnv() {
	if viper.IsSet("whatsapp_calls_max_duration_minutes") {
		config.WhatsappCallsMaxDurationMinutes = viper.GetInt("whatsapp_calls_max_duration_minutes")
	}
	if viper.IsSet("whatsapp_calls_recording_url_ttl_minutes") {
		config.WhatsappCallsRecordingURLTTLMinutes = viper.GetInt("whatsapp_calls_recording_url_ttl_minutes")
	}
	if viper.IsSet("whatsapp_calls_recording_retention_days") {
		config.WhatsappCallsRecordingRetentionDays = viper.GetInt("whatsapp_calls_recording_retention_days")
	}
	if viper.IsSet("whatsapp_calls_rtc_port") {
		config.WhatsappCallsRTCPort = viper.GetInt("whatsapp_calls_rtc_port")
	}
	if env := viper.GetString("whatsapp_calls_rtc_public_ip"); env != "" {
		config.WhatsappCallsRTCPublicIP = env
	}
	if viper.IsSet("whatsapp_calls_rtc_grace_seconds") {
		config.WhatsappCallsRTCGraceSeconds = viper.GetInt("whatsapp_calls_rtc_grace_seconds")
	}
}

func initCallsFlags() {
	flags := rootCmd.PersistentFlags()
	flags.IntVarP(&config.WhatsappCallsMaxDurationMinutes, "calls-max-duration-minutes", "", config.WhatsappCallsMaxDurationMinutes,
		`hang up a call after this many minutes (0: no limit) --calls-max-duration-minutes <n> | example: --calls-max-duration-minutes=120`)
	flags.IntVarP(&config.WhatsappCallsRecordingURLTTLMinutes, "calls-recording-url-ttl-minutes", "", config.WhatsappCallsRecordingURLTTLMinutes,
		`validity of the signed recording link --calls-recording-url-ttl-minutes <n> | example: --calls-recording-url-ttl-minutes=1440`)
	flags.IntVarP(&config.WhatsappCallsRecordingRetentionDays, "calls-recording-retention-days", "", config.WhatsappCallsRecordingRetentionDays,
		`delete call recordings older than this (0: keep) --calls-recording-retention-days <n> | example: --calls-recording-retention-days=7`)
	flags.IntVarP(&config.WhatsappCallsRTCPort, "calls-rtc-port", "", config.WhatsappCallsRTCPort,
		`UDP port of the browser leg --calls-rtc-port <n> | example: --calls-rtc-port=3478`)
	flags.StringVarP(&config.WhatsappCallsRTCPublicIP, "calls-rtc-public-ip", "", config.WhatsappCallsRTCPublicIP,
		`public IPv4 announced to browsers --calls-rtc-public-ip <ip> | example: --calls-rtc-public-ip=203.0.113.7`)
	flags.IntVarP(&config.WhatsappCallsRTCGraceSeconds, "calls-rtc-grace-seconds", "", config.WhatsappCallsRTCGraceSeconds,
		`hang up after the browser leg is down this long --calls-rtc-grace-seconds <n> | example: --calls-rtc-grace-seconds=10`)
}
