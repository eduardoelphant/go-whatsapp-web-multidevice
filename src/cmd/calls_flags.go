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
}

func initCallsFlags() {
	flags := rootCmd.PersistentFlags()
	flags.IntVarP(&config.WhatsappCallsMaxDurationMinutes, "calls-max-duration-minutes", "", config.WhatsappCallsMaxDurationMinutes,
		`hang up a call after this many minutes (0: no limit) --calls-max-duration-minutes <n> | example: --calls-max-duration-minutes=120`)
}
