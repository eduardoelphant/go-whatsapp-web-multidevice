package config

import "testing"

// The fork's settings ship safe: nothing that changes behavior is on by default.
func TestForkSettingDefaults(t *testing.T) {
	if WhatsappReachoutGuard {
		t.Error("WhatsappReachoutGuard must default to off")
	}
	if WhatsappReachoutSuspectMinutes != 30 {
		t.Errorf("WhatsappReachoutSuspectMinutes = %d, want 30", WhatsappReachoutSuspectMinutes)
	}
	if WhatsappWatchdogIntervalSeconds != 120 {
		t.Errorf("WhatsappWatchdogIntervalSeconds = %d, want 120", WhatsappWatchdogIntervalSeconds)
	}
	if WhatsappUserCheckMinIntervalMs != 500 {
		t.Errorf("WhatsappUserCheckMinIntervalMs = %d, want 500", WhatsappUserCheckMinIntervalMs)
	}
	if AppStaticsAuth {
		t.Error("AppStaticsAuth must default to off")
	}
}
