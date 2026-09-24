package baileysimport

import (
	"fmt"
	"strconv"
	"strings"
)

// SignalAddress converts a libsignal-node protocol address ("<name>.<device>",
// e.g. "5511999999999.0" or "123456_1.3") into the whatsmeow form
// ("<name>:<device>"). The name keeps its "_<domain>" suffix, which whatsmeow
// writes the same way (JID.SignalAddressUser), so only the last dot changes.
func SignalAddress(baileysAddress string) (string, error) {
	idx := strings.LastIndexByte(baileysAddress, '.')
	if idx <= 0 || idx == len(baileysAddress)-1 {
		return "", fmt.Errorf("invalid signal address %q", baileysAddress)
	}
	name, device := baileysAddress[:idx], baileysAddress[idx+1:]
	if strings.ContainsAny(name, ".:@") {
		return "", fmt.Errorf("invalid signal address %q", baileysAddress)
	}
	if _, err := strconv.ParseUint(device, 10, 16); err != nil {
		return "", fmt.Errorf("invalid device in signal address %q", baileysAddress)
	}
	return name + ":" + device, nil
}

// SenderKeyName splits a Baileys sender key id ("<group>::<user>::<device>")
// into the whatsmeow chat_id and sender_id ("<user>:<device>"). The
// useMultiFileAuthState file-name form, where ":" is written as "-"
// ("<group>--<user>--<device>"), is accepted as well.
func SenderKeyName(id string) (group, sender string, err error) {
	for _, sep := range []string{"::", "--"} {
		last := strings.LastIndex(id, sep)
		if last <= 0 {
			continue
		}
		prev := strings.LastIndex(id[:last], sep)
		if prev <= 0 {
			continue
		}
		group = id[:prev]
		user := id[prev+len(sep) : last]
		device := id[last+len(sep):]
		if !strings.Contains(group, "@") || user == "" || strings.ContainsAny(user, ".:@") {
			continue
		}
		if _, convErr := strconv.ParseUint(device, 10, 16); convErr != nil {
			continue
		}
		return group, user + ":" + device, nil
	}
	return "", "", fmt.Errorf("invalid sender key id %q", id)
}
