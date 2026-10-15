package cmd

import (
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/spf13/viper"
)

func TestInitCallsEnv(t *testing.T) {
	orig := config.WhatsappCallsMaxDurationMinutes
	t.Cleanup(func() { config.WhatsappCallsMaxDurationMinutes = orig })
	viper.Set("whatsapp_calls_max_duration_minutes", 30)
	t.Cleanup(func() { viper.Set("whatsapp_calls_max_duration_minutes", orig) })

	initCallsEnv()
	if config.WhatsappCallsMaxDurationMinutes != 30 {
		t.Fatalf("got %d, want 30", config.WhatsappCallsMaxDurationMinutes)
	}
	if rootCmd.PersistentFlags().Lookup("calls-max-duration-minutes") == nil {
		t.Fatal("flag not registered")
	}
}
