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

func TestInitCallsEnvRTC(t *testing.T) {
	port, ip, grace := config.WhatsappCallsRTCPort, config.WhatsappCallsRTCPublicIP, config.WhatsappCallsRTCGraceSeconds
	t.Cleanup(func() {
		config.WhatsappCallsRTCPort, config.WhatsappCallsRTCPublicIP, config.WhatsappCallsRTCGraceSeconds = port, ip, grace
		viper.Set("whatsapp_calls_rtc_port", port)
		viper.Set("whatsapp_calls_rtc_public_ip", ip)
		viper.Set("whatsapp_calls_rtc_grace_seconds", grace)
	})
	viper.Set("whatsapp_calls_rtc_port", 40000)
	viper.Set("whatsapp_calls_rtc_public_ip", "203.0.113.7")
	viper.Set("whatsapp_calls_rtc_grace_seconds", 5)

	initCallsEnv()
	if config.WhatsappCallsRTCPort != 40000 || config.WhatsappCallsRTCPublicIP != "203.0.113.7" || config.WhatsappCallsRTCGraceSeconds != 5 {
		t.Fatalf("got %d %q %d", config.WhatsappCallsRTCPort, config.WhatsappCallsRTCPublicIP, config.WhatsappCallsRTCGraceSeconds)
	}
	for _, name := range []string{"calls-rtc-port", "calls-rtc-public-ip", "calls-rtc-grace-seconds"} {
		if rootCmd.PersistentFlags().Lookup(name) == nil {
			t.Fatalf("flag %s not registered", name)
		}
	}
}

func TestInitCallsEnvMaxTotal(t *testing.T) {
	orig := config.WhatsappCallsMaxTotal
	t.Cleanup(func() {
		config.WhatsappCallsMaxTotal = orig
		viper.Set("whatsapp_calls_max_total", nil)
	})
	viper.Set("whatsapp_calls_max_total", 6)

	initCallsEnv()
	if config.WhatsappCallsMaxTotal != 6 {
		t.Fatalf("got %d, want 6", config.WhatsappCallsMaxTotal)
	}
	if rootCmd.PersistentFlags().Lookup("calls-max-total") == nil {
		t.Fatal("flag not registered")
	}
}
