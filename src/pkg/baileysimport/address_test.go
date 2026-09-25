package baileysimport

import "testing"

func TestSignalAddress(t *testing.T) {
	cases := map[string]string{
		"5511999999999.0":  "5511999999999:0",
		"5511999999999.12": "5511999999999:12",
		"123_1.3":          "123_1:3",
		"123456_128.99":    "123456_128:99",
	}
	for in, want := range cases {
		got, err := SignalAddress(in)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "5511", ".0", "5511.", "5511.x", "5511.1.2", "a:b.0", "a@s.whatsapp.net.0"} {
		if _, err := SignalAddress(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestSenderKeyName(t *testing.T) {
	cases := []struct{ in, group, sender string }{
		{"120363000000000001@g.us::5511999999999::0", "120363000000000001@g.us", "5511999999999:0"},
		{"120363000000000001@g.us::123_1::7", "120363000000000001@g.us", "123_1:7"},
		// useMultiFileAuthState file-name form (":" written as "-"), with an
		// old-style group id that itself contains a dash.
		{"5511999999999-1612345678@g.us--5511888888888--2", "5511999999999-1612345678@g.us", "5511888888888:2"},
		{"status@broadcast::5511999999999::0", "status@broadcast", "5511999999999:0"},
	}
	for _, c := range cases {
		group, sender, err := SenderKeyName(c.in)
		if err != nil || group != c.group || sender != c.sender {
			t.Errorf("%s: got %q %q %v", c.in, group, sender, err)
		}
	}
	for _, bad := range []string{"", "group::user", "nogroup::5511::0", "g@g.us::5511::x", "g@g.us::::0"} {
		if _, _, err := SenderKeyName(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestSplitKeyName(t *testing.T) {
	cases := map[string][2]string{
		"pre-key-12":                          {KeyTypePreKey, "12"},
		"session-123_1.3":                     {KeyTypeSession, "123_1.3"},
		"sender-key-g@g.us::1::0":             {KeyTypeSenderKey, "g@g.us::1::0"},
		"sender-key-memory-g@g.us":            {KeyTypeSenderKeyMemory, "g@g.us"},
		"app-state-sync-key-AAAAAA==":         {KeyTypeAppStateSyncKey, "AAAAAA=="},
		"app-state-sync-version-regular_high": {KeyTypeAppStateSyncVersion, "regular_high"},
		"lid-mapping-123_reverse":             {KeyTypeLIDMapping, "123_reverse"},
		"device-list-5511":                    {KeyTypeDeviceList, "5511"},
		"tctoken-123@lid":                     {KeyTypeTCToken, "123@lid"},
		"identity-key-5511.0":                 {keyTypeUnknown, "identity-key-5511.0"},
		"session-":                            {keyTypeUnknown, "session-"},
	}
	for in, want := range cases {
		typ, id := splitKeyName(in)
		if typ != want[0] || id != want[1] {
			t.Errorf("%s: got %s %s", in, typ, id)
		}
	}
}
