package config

import "testing"

func TestCallsAllowed(t *testing.T) {
	origEnabled, origDevices := WhatsappCallsEnabled, WhatsappCallsDevices
	t.Cleanup(func() { WhatsappCallsEnabled, WhatsappCallsDevices = origEnabled, origDevices })

	WhatsappCallsEnabled = false
	WhatsappCallsDevices = nil
	if CallsAllowed("slot-a", "5511900000000", "5511900000000:27@s.whatsapp.net") {
		t.Fatal("flag off must deny")
	}

	WhatsappCallsEnabled = true
	if !CallsAllowed("slot-a", "5511900000000", "5511900000000:27@s.whatsapp.net") {
		t.Fatal("enabled with an empty list must allow every device")
	}

	WhatsappCallsDevices = []string{" 5511900000000 ", "slot-b"}
	cases := []struct {
		slot, user, jid string
		want            bool
	}{
		{"slot-x", "5511900000000", "5511900000000:27@s.whatsapp.net", true},
		{"slot-b", "", "", true},
		{"slot-c", "5527900000000", "5527900000000:17@s.whatsapp.net", false},
		{"5511900000000:27@s.whatsapp.net", "", "", false},
	}
	for _, c := range cases {
		if got := CallsAllowed(c.slot, c.user, c.jid); got != c.want {
			t.Errorf("CallsAllowed(%q,%q,%q) = %v, want %v", c.slot, c.user, c.jid, got, c.want)
		}
	}

	WhatsappCallsDevices = []string{"5511900000000:27@s.whatsapp.net"}
	if !CallsAllowed("slot-x", "5511900000000", "5511900000000:27@s.whatsapp.net") {
		t.Fatal("a full JID in the list must match")
	}
}

func TestCallsStage2bDefaults(t *testing.T) {
	if WhatsappCallsMaxDurationMinutes != 120 {
		t.Fatalf("WhatsappCallsMaxDurationMinutes = %d, want 120", WhatsappCallsMaxDurationMinutes)
	}
	if WhatsappCallsRecordingURLTTLMinutes != 1440 || WhatsappCallsRecordingRetentionDays != 7 {
		t.Fatalf("recording defaults %d min / %d days, want 1440 / 7", WhatsappCallsRecordingURLTTLMinutes, WhatsappCallsRecordingRetentionDays)
	}
}
