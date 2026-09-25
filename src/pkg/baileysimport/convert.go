package baileysimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// Options controls what Convert keeps.
type Options struct {
	// SkipSignalSessions leaves out 1:1 Signal sessions and the identity keys
	// derived from them. whatsmeow then fetches fresh pre-key bundles before
	// sending, and peers re-establish sessions through retry receipts.
	SkipSignalSessions bool
}

// PreKey is a one-time pre-key to insert with its original id.
type PreKey struct {
	ID       uint32
	Private  [32]byte
	Uploaded bool
}

// SenderKey is a converted group sender key record.
type SenderKey struct {
	ChatID   string
	SenderID string
	Record   []byte
}

// AppStateSyncKey is a converted app state sync key.
type AppStateSyncKey struct {
	ID  []byte
	Key store.AppStateSyncKey
}

// Plan is everything Convert produced, ready to be written by Write.
type Plan struct {
	Device           *DeviceSpec
	PreKeys          []PreKey
	Sessions         map[string][]byte
	Identities       map[string][32]byte
	SenderKeys       []SenderKey
	AppStateSyncKeys []AppStateSyncKey
	LIDMappings      []store.LIDMapping
	PrivacyTokens    []store.PrivacyToken

	// Ignored counts key entries dropped on purpose, by key type.
	Ignored map[string]int
	// Skipped counts entries that could not be imported, by reason.
	Skipped map[string]int
	// Trimmed counts state dropped to fit go.mau.fi/libsignal limits.
	Trimmed sessionStats
	// PreviousSessionStates counts closed sessions kept as previous states.
	PreviousSessionStates int
}

// Convert maps a parsed Baileys auth state to whatsmeow data. It never
// touches a database; Write persists the result.
func Convert(state *AuthState, opts Options) (*Plan, error) {
	if state == nil {
		return nil, fmt.Errorf("nil auth state")
	}
	device, err := convertDevice(state.Creds)
	if err != nil {
		return nil, err
	}
	plan := &Plan{
		Device:     device,
		Sessions:   map[string][]byte{},
		Identities: map[string][32]byte{},
		Ignored:    map[string]int{},
		Skipped:    map[string]int{},
	}
	local := localIdentity{Public: *device.IdentityKey.Pub, RegistrationID: device.RegistrationID}

	names := make([]string, 0, len(state.Keys))
	for name := range state.Keys {
		names = append(names, name)
	}
	sort.Strings(names)

	lids := newLIDCollector()
	for _, name := range names {
		raw := state.Keys[name]
		if isJSONNull(raw) {
			continue
		}
		keyType, id := splitKeyName(name)
		var err error
		switch keyType {
		case KeyTypePreKey:
			err = plan.addPreKey(id, raw, state.Creds.FirstUnuploadedPreKeyID)
		case KeyTypeSession:
			if opts.SkipSignalSessions {
				plan.Ignored[KeyTypeSession]++
				continue
			}
			err = plan.addSession(id, raw, local)
		case KeyTypeSenderKey:
			err = plan.addSenderKey(id, raw)
		case KeyTypeAppStateSyncKey:
			err = plan.addAppStateSyncKey(id, raw)
		case KeyTypeLIDMapping:
			err = lids.add(id, raw)
		case KeyTypeTCToken:
			err = plan.addPrivacyToken(id, raw)
		case KeyTypeAppStateSyncVersion, KeyTypeDeviceList, KeyTypeSenderKeyMemory:
			plan.Ignored[keyType]++
		default:
			plan.Ignored[keyTypeUnknown]++
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	plan.LIDMappings = lids.mappings()
	sort.Slice(plan.PreKeys, func(i, j int) bool { return plan.PreKeys[i].ID < plan.PreKeys[j].ID })
	return plan, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || strings.TrimSpace(string(raw)) == "null"
}

func (p *Plan) addPreKey(id string, raw json.RawMessage, firstUnuploaded uint32) error {
	keyID, err := strconv.ParseUint(id, 10, 32)
	if err != nil || keyID >= maxSignalKeyID {
		return fmt.Errorf("invalid pre-key id %q", id)
	}
	value, err := unwrapJSON(raw, false)
	if err != nil {
		return err
	}
	var kp KeyPair
	if err := json.Unmarshal(value, &kp); err != nil {
		return err
	}
	pair, err := keyPairFromBaileys("pre-key", kp)
	if err != nil {
		return err
	}
	p.PreKeys = append(p.PreKeys, PreKey{
		ID:       uint32(keyID),
		Private:  *pair.Priv,
		Uploaded: uint32(keyID) < firstUnuploaded,
	})
	return nil
}

func (p *Plan) addSession(id string, raw json.RawMessage, local localIdentity) error {
	address, err := SignalAddress(id)
	if err != nil {
		return err
	}
	value, err := unwrapJSON(raw, true)
	if err != nil {
		return err
	}
	converted, err := convertSessionRecord(value, local)
	if errors.Is(err, ErrNoOpenSession) {
		p.Skipped["session without open state"]++
		return nil
	} else if err != nil {
		return err
	}
	p.Sessions[address] = converted.Record
	p.Identities[address] = converted.RemoteIdentity
	p.Trimmed.add(converted.Stats)
	p.PreviousSessionStates += converted.Stats.PreviousStates
	p.Trimmed.PreviousStates = 0
	return nil
}

func (p *Plan) addSenderKey(id string, raw json.RawMessage) error {
	chatID, senderID, err := SenderKeyName(id)
	if err != nil {
		return err
	}
	value, err := unwrapJSON(raw, true)
	if err != nil {
		return err
	}
	serialized, dropped, err := convertSenderKeyRecord(value)
	if err != nil {
		return err
	}
	p.Trimmed.DroppedMessageKeys += dropped
	p.SenderKeys = append(p.SenderKeys, SenderKey{ChatID: chatID, SenderID: senderID, Record: serialized})
	return nil
}

// nodeAppStateSyncKeyData is proto.Message.IAppStateSyncKeyData as persisted.
type nodeAppStateSyncKeyData struct {
	KeyData     Bytes `json:"keyData"`
	Fingerprint *struct {
		RawID         *uint32  `json:"rawId"`
		CurrentIndex  *uint32  `json:"currentIndex"`
		DeviceIndexes []uint32 `json:"deviceIndexes"`
	} `json:"fingerprint"`
	Timestamp jsonInt64 `json:"timestamp"`
}

func (p *Plan) addAppStateSyncKey(id string, raw json.RawMessage) error {
	// useMultiFileAuthState writes "/" as "__" in file names; standard base64
	// never contains "_", so the mapping back is unambiguous.
	keyID, err := decodeBase64(strings.ReplaceAll(id, "__", "/"))
	if err != nil || len(keyID) == 0 {
		return fmt.Errorf("invalid app state sync key id %q", id)
	}
	value, err := unwrapJSON(raw, false)
	if err != nil {
		return err
	}
	var data nodeAppStateSyncKeyData
	if err := json.Unmarshal(value, &data); err != nil {
		return err
	}
	if len(data.KeyData) == 0 {
		return fmt.Errorf("app state sync key has no keyData")
	}
	fingerprint := &waE2E.AppStateSyncKeyFingerprint{}
	if f := data.Fingerprint; f != nil {
		fingerprint.RawID = f.RawID
		fingerprint.CurrentIndex = f.CurrentIndex
		fingerprint.DeviceIndexes = f.DeviceIndexes
	}
	marshaled, err := proto.Marshal(fingerprint)
	if err != nil {
		return fmt.Errorf("marshal fingerprint: %w", err)
	}
	if marshaled == nil {
		marshaled = []byte{}
	}
	p.AppStateSyncKeys = append(p.AppStateSyncKeys, AppStateSyncKey{
		ID: keyID,
		Key: store.AppStateSyncKey{
			Data:        data.KeyData,
			Fingerprint: marshaled,
			Timestamp:   int64(data.Timestamp),
		},
	})
	return nil
}

func (p *Plan) addPrivacyToken(id string, raw json.RawMessage) error {
	jid, err := types.ParseJID(id)
	if err != nil || jid.User == "" {
		return fmt.Errorf("invalid tctoken jid %q", id)
	}
	value, err := unwrapJSON(raw, false)
	if err != nil {
		return err
	}
	var token struct {
		Token     Bytes     `json:"token"`
		Timestamp jsonInt64 `json:"timestamp"`
	}
	if err := json.Unmarshal(value, &token); err != nil {
		return err
	}
	if len(token.Token) == 0 {
		p.Skipped["tctoken without token"]++
		return nil
	}
	if token.Timestamp <= 0 {
		// whatsmeow expires tokens by timestamp; one without it cannot be aged.
		p.Skipped["tctoken without timestamp"]++
		return nil
	}
	p.PrivacyTokens = append(p.PrivacyTokens, store.PrivacyToken{
		User:      jid.ToNonAD(),
		Token:     token.Token,
		Timestamp: time.Unix(int64(token.Timestamp), 0),
	})
	return nil
}

// lidCollector merges Baileys' forward ("<pn>" -> "<lid>") and reverse
// ("<lid>_reverse" -> "<pn>") LID mapping entries.
type lidCollector struct {
	pnToLID map[string]string
}

func newLIDCollector() *lidCollector {
	return &lidCollector{pnToLID: map[string]string{}}
}

func (c *lidCollector) add(id string, raw json.RawMessage) error {
	value, err := unwrapString(raw)
	if err != nil {
		return err
	}
	if lidUser, ok := strings.CutSuffix(id, "_reverse"); ok {
		if !isDigits(lidUser) || !isDigits(value) {
			return fmt.Errorf("invalid reverse lid mapping %q -> %q", id, value)
		}
		if _, exists := c.pnToLID[value]; !exists {
			c.pnToLID[value] = lidUser
		}
		return nil
	}
	if !isDigits(id) || !isDigits(value) {
		return fmt.Errorf("invalid lid mapping %q -> %q", id, value)
	}
	c.pnToLID[id] = value
	return nil
}

func (c *lidCollector) mappings() []store.LIDMapping {
	pns := make([]string, 0, len(c.pnToLID))
	for pn := range c.pnToLID {
		pns = append(pns, pn)
	}
	sort.Strings(pns)
	out := make([]store.LIDMapping, 0, len(pns))
	for _, pn := range pns {
		out = append(out, store.LIDMapping{
			LID: types.NewJID(c.pnToLID[pn], types.HiddenUserServer),
			PN:  types.NewJID(pn, types.DefaultUserServer),
		})
	}
	return out
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Summary is a key-free description of a Plan.
type Summary struct {
	JID                   string         `json:"jid"`
	LID                   string         `json:"lid,omitempty"`
	PushName              string         `json:"push_name,omitempty"`
	Platform              string         `json:"platform,omitempty"`
	PreKeys               int            `json:"pre_keys"`
	PreKeysUploaded       int            `json:"pre_keys_uploaded"`
	Sessions              int            `json:"sessions"`
	PreviousSessionStates int            `json:"previous_session_states"`
	IdentityKeys          int            `json:"identity_keys"`
	SenderKeys            int            `json:"sender_keys"`
	AppStateSyncKeys      int            `json:"app_state_sync_keys"`
	LIDMappings           int            `json:"lid_mappings"`
	PrivacyTokens         int            `json:"privacy_tokens"`
	Ignored               map[string]int `json:"ignored,omitempty"`
	Skipped               map[string]int `json:"skipped,omitempty"`
	DroppedSessionStates  int            `json:"dropped_session_states,omitempty"`
	DroppedChains         int            `json:"dropped_chains,omitempty"`
	DroppedMessageKeys    int            `json:"dropped_message_keys,omitempty"`
}

// Summary returns counts only; it never includes key material.
func (p *Plan) Summary() Summary {
	s := Summary{
		JID:                   p.Device.ID.String(),
		PushName:              p.Device.PushName,
		Platform:              p.Device.Platform,
		PreKeys:               len(p.PreKeys),
		Sessions:              len(p.Sessions),
		PreviousSessionStates: p.PreviousSessionStates,
		IdentityKeys:          len(p.Identities),
		SenderKeys:            len(p.SenderKeys),
		AppStateSyncKeys:      len(p.AppStateSyncKeys),
		LIDMappings:           len(p.LIDMappings),
		PrivacyTokens:         len(p.PrivacyTokens),
		Ignored:               p.Ignored,
		Skipped:               p.Skipped,
		DroppedSessionStates:  p.Trimmed.DroppedStates,
		DroppedChains:         p.Trimmed.DroppedChains,
		DroppedMessageKeys:    p.Trimmed.DroppedMessageKeys,
	}
	if !p.Device.LID.IsEmpty() {
		s.LID = p.Device.LID.String()
	}
	for _, k := range p.PreKeys {
		if k.Uploaded {
			s.PreKeysUploaded++
		}
	}
	return s
}
