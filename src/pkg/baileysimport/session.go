package baileysimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"go.mau.fi/libsignal/kdf"
	"go.mau.fi/libsignal/keys/chain"
	"go.mau.fi/libsignal/keys/message"
	"go.mau.fi/libsignal/state/record"
	"go.mau.fi/libsignal/util/optional"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/util/keys"
)

// Limits of go.mau.fi/libsignal (state/record): archived session states,
// receiver chains per state and stored message keys per chain.
const (
	maxPreviousStates     = 40
	maxReceiverChains     = 5
	maxMessageKeysPerLink = 2000
	signalSessionVersion  = 3
	chainTypeSending      = 1
	chainTypeReceiving    = 2
)

// ErrNoOpenSession marks a libsignal-node record whose sessions are all
// closed. Baileys would refuse to encrypt with it and fetch a new pre-key
// bundle; in whatsmeow the mere presence of a row suppresses that fetch, so
// such records are skipped instead of imported.
var ErrNoOpenSession = errors.New("record has no open session")

// nodeSessionRecord is libsignal-node's SessionRecord.serialize() output.
type nodeSessionRecord struct {
	Version        string          `json:"version"`
	RegistrationID *uint32         `json:"registrationId"`
	Sessions       json.RawMessage `json:"_sessions"`
}

// nodeSessionEntry is libsignal-node's SessionEntry.serialize() output.
type nodeSessionEntry struct {
	RegistrationID *uint32 `json:"registrationId"`
	CurrentRatchet struct {
		EphemeralKeyPair struct {
			PubKey  Bytes `json:"pubKey"`
			PrivKey Bytes `json:"privKey"`
		} `json:"ephemeralKeyPair"`
		LastRemoteEphemeralKey Bytes  `json:"lastRemoteEphemeralKey"`
		PreviousCounter        uint32 `json:"previousCounter"`
		RootKey                Bytes  `json:"rootKey"`
	} `json:"currentRatchet"`
	IndexInfo struct {
		BaseKey           Bytes     `json:"baseKey"`
		BaseKeyType       int       `json:"baseKeyType"`
		Closed            jsonInt64 `json:"closed"`
		Used              jsonInt64 `json:"used"`
		Created           jsonInt64 `json:"created"`
		RemoteIdentityKey Bytes     `json:"remoteIdentityKey"`
	} `json:"indexInfo"`
	Chains        json.RawMessage `json:"_chains"`
	PendingPreKey *struct {
		SignedKeyID uint32  `json:"signedKeyId"`
		BaseKey     Bytes   `json:"baseKey"`
		PreKeyID    *uint32 `json:"preKeyId"`
	} `json:"pendingPreKey"`
}

// nodeChain is one entry of SessionEntry._chains. chainKey.counter is the
// index of the last message key derived (-1 when none), and chainKey.key is
// absent once libsignal-node closes a receiving chain.
type nodeChain struct {
	ChainKey struct {
		Counter jsonInt64 `json:"counter"`
		Key     Bytes     `json:"key"`
	} `json:"chainKey"`
	ChainType   int              `json:"chainType"`
	MessageKeys map[string]Bytes `json:"messageKeys"`
}

// sessionStats counts what a conversion had to leave out.
type sessionStats struct {
	PreviousStates     int
	DroppedStates      int
	DroppedChains      int
	DroppedMessageKeys int
}

func (s *sessionStats) add(o sessionStats) {
	s.PreviousStates += o.PreviousStates
	s.DroppedStates += o.DroppedStates
	s.DroppedChains += o.DroppedChains
	s.DroppedMessageKeys += o.DroppedMessageKeys
}

type localIdentity struct {
	Public         [32]byte
	RegistrationID uint32
}

type convertedSession struct {
	Record         []byte
	RemoteIdentity [32]byte
	Stats          sessionStats
}

// convertSessionRecord turns a libsignal-node session record into the
// serialized go.mau.fi/libsignal record whatsmeow stores in
// whatsmeow_sessions.session (JSON of record.SessionStructure; whatsmeow's
// "SignalProtobufSerializer" only uses protobuf for wire messages).
//
// The open session (indexInfo.closed == -1) becomes the current state; closed
// ones become previous states, most recently used first, which is the order
// libsignal-node tries them in when decrypting.
func convertSessionRecord(raw json.RawMessage, local localIdentity) (*convertedSession, error) {
	var rec nodeSessionRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("invalid session record: %w", err)
	}
	if rec.Version != "" && rec.Version != "v1" {
		return nil, fmt.Errorf("unsupported session record version %q", rec.Version)
	}
	entries, err := decodeOrderedObject(rec.Sessions)
	if err != nil {
		return nil, fmt.Errorf("invalid _sessions: %w", err)
	}

	type candidate struct {
		state *record.StateStructure
		entry *nodeSessionEntry
		stats sessionStats
	}
	var open []candidate
	var closed []candidate
	var stats sessionStats
	for _, e := range entries {
		var entry nodeSessionEntry
		if err := json.Unmarshal(e.Value, &entry); err != nil {
			return nil, fmt.Errorf("session %s: %w", e.Key, err)
		}
		regID := entry.RegistrationID
		if regID == nil {
			regID = rec.RegistrationID
		}
		state, st, err := convertSessionEntry(&entry, regID, local)
		isOpen := entry.IndexInfo.Closed == -1
		if err != nil {
			if isOpen {
				return nil, fmt.Errorf("open session: %w", err)
			}
			stats.DroppedStates++
			continue
		}
		c := candidate{state: state, entry: &entry, stats: st}
		if isOpen {
			open = append(open, c)
		} else {
			closed = append(closed, c)
		}
	}
	if len(open) == 0 {
		return nil, ErrNoOpenSession
	}
	byUsedDesc := func(list []candidate) {
		sort.SliceStable(list, func(i, j int) bool { return list[i].entry.IndexInfo.Used > list[j].entry.IndexInfo.Used })
	}
	byUsedDesc(open)
	current := open[0]
	previous := append(open[1:], closed...)
	byUsedDesc(previous)
	if len(previous) > maxPreviousStates {
		stats.DroppedStates += len(previous) - maxPreviousStates
		previous = previous[:maxPreviousStates]
	}

	structure := &record.SessionStructure{SessionState: current.state}
	stats.add(current.stats)
	for _, p := range previous {
		structure.PreviousStates = append(structure.PreviousStates, p.state)
		stats.add(p.stats)
	}
	stats.PreviousStates = len(previous)

	serializer := store.SignalProtobufSerializer
	serialized := serializer.Session.Serialize(structure)
	if _, err := record.NewSessionFromBytes(serialized, serializer.Session, serializer.State); err != nil {
		return nil, fmt.Errorf("converted session does not load: %w", err)
	}
	remote, err := unprefixedPublicKey(current.entry.IndexInfo.RemoteIdentityKey)
	if err != nil {
		return nil, fmt.Errorf("remoteIdentityKey: %w", err)
	}
	return &convertedSession{Record: serialized, RemoteIdentity: [32]byte(remote), Stats: stats}, nil
}

func convertSessionEntry(e *nodeSessionEntry, remoteRegID *uint32, local localIdentity) (*record.StateStructure, sessionStats, error) {
	var stats sessionStats
	ourRatchetPub, err := normalizePublicKey(e.CurrentRatchet.EphemeralKeyPair.PubKey)
	if err != nil {
		return nil, stats, fmt.Errorf("currentRatchet.ephemeralKeyPair.pubKey: %w", err)
	}
	ourRatchetPriv, err := privateKey(e.CurrentRatchet.EphemeralKeyPair.PrivKey)
	if err != nil {
		return nil, stats, fmt.Errorf("currentRatchet.ephemeralKeyPair.privKey: %w", err)
	}
	if derived := keys.NewKeyPairFromPrivateKey(ourRatchetPriv); !bytes.Equal(derived.Pub[:], ourRatchetPub[1:]) {
		return nil, stats, fmt.Errorf("currentRatchet.ephemeralKeyPair does not match")
	}
	if len(e.CurrentRatchet.RootKey) != 32 {
		return nil, stats, fmt.Errorf("currentRatchet.rootKey has %d bytes, want 32", len(e.CurrentRatchet.RootKey))
	}
	remoteIdentity, err := normalizePublicKey(e.IndexInfo.RemoteIdentityKey)
	if err != nil {
		return nil, stats, fmt.Errorf("indexInfo.remoteIdentityKey: %w", err)
	}
	baseKey, err := normalizePublicKey(e.IndexInfo.BaseKey)
	if err != nil {
		return nil, stats, fmt.Errorf("indexInfo.baseKey: %w", err)
	}

	chains, err := decodeOrderedObject(e.Chains)
	if err != nil {
		return nil, stats, fmt.Errorf("invalid _chains: %w", err)
	}
	var sender *record.ChainStructure
	var receivers []*record.ChainStructure
	for _, c := range chains {
		ratchetKey, err := decodeBase64(c.Key)
		if err != nil {
			return nil, stats, fmt.Errorf("chain id: %w", err)
		}
		ratchetKey, err = normalizePublicKey(ratchetKey)
		if err != nil {
			return nil, stats, fmt.Errorf("chain id: %w", err)
		}
		var nc nodeChain
		if err := json.Unmarshal(c.Value, &nc); err != nil {
			return nil, stats, fmt.Errorf("chain: %w", err)
		}
		chainKey, err := convertChainKey(nc)
		if err != nil {
			return nil, stats, err
		}
		switch nc.ChainType {
		case chainTypeSending:
			if !bytes.Equal(ratchetKey, ourRatchetPub) {
				// libsignal-node deletes the old sending chain when it ratchets;
				// a stray one can never be used again.
				stats.DroppedChains++
				continue
			}
			if chainKey.Key == nil {
				return nil, stats, fmt.Errorf("sending chain has no chain key")
			}
			sender = &record.ChainStructure{
				SenderRatchetKeyPublic:  ratchetKey,
				SenderRatchetKeyPrivate: bytes.Clone(ourRatchetPriv[:]),
				ChainKey:                chainKey,
				MessageKeys:             []*message.KeysStructure{},
			}
		case chainTypeReceiving:
			msgKeys, dropped, err := convertMessageKeys(nc.MessageKeys)
			if err != nil {
				return nil, stats, err
			}
			stats.DroppedMessageKeys += dropped
			receivers = append(receivers, &record.ChainStructure{
				SenderRatchetKeyPublic: ratchetKey,
				ChainKey:               chainKey,
				MessageKeys:            msgKeys,
			})
		default:
			return nil, stats, fmt.Errorf("unknown chainType %d", nc.ChainType)
		}
	}
	if sender == nil {
		return nil, stats, fmt.Errorf("no sending chain for the current ratchet key")
	}
	// libsignal-node never drops receiving chains (it only closes them);
	// go.mau.fi/libsignal keeps the newest five. _chains keeps insertion order,
	// so the oldest are at the front.
	if len(receivers) > maxReceiverChains {
		for _, dropped := range receivers[:len(receivers)-maxReceiverChains] {
			stats.DroppedChains++
			stats.DroppedMessageKeys += len(dropped.MessageKeys)
		}
		receivers = receivers[len(receivers)-maxReceiverChains:]
	}

	var regID uint32
	if remoteRegID != nil {
		regID = *remoteRegID
	}
	state := &record.StateStructure{
		LocalIdentityPublic:  prefixedPublicKey(local.Public[:]),
		LocalRegistrationID:  local.RegistrationID,
		PreviousCounter:      e.CurrentRatchet.PreviousCounter,
		ReceiverChains:       receivers,
		RemoteIdentityPublic: remoteIdentity,
		RemoteRegistrationID: regID,
		RootKey:              bytes.Clone(e.CurrentRatchet.RootKey),
		SenderBaseKey:        baseKey,
		SenderChain:          sender,
		SessionVersion:       signalSessionVersion,
	}
	if p := e.PendingPreKey; p != nil {
		pendingBase, err := normalizePublicKey(p.BaseKey)
		if err != nil {
			return nil, stats, fmt.Errorf("pendingPreKey.baseKey: %w", err)
		}
		preKeyID := optional.NewEmptyUint32()
		if p.PreKeyID != nil && *p.PreKeyID != 0 {
			preKeyID = optional.NewOptionalUint32(*p.PreKeyID)
		}
		state.PendingPreKey = &record.PendingPreKeyStructure{
			PreKeyID:       preKeyID,
			SignedPreKeyID: p.SignedKeyID,
			BaseKey:        pendingBase,
		}
	}
	return state, stats, nil
}

// convertChainKey maps libsignal-node's {counter, key} to go.mau.fi/libsignal's
// {index, key}. libsignal-node's counter is the last derived index (-1 when
// none), while go.mau.fi/libsignal's index is the next one: index = counter+1.
func convertChainKey(nc nodeChain) (*chain.KeyStructure, error) {
	counter := int64(nc.ChainKey.Counter)
	if counter < -1 || counter >= 1<<32-1 {
		return nil, fmt.Errorf("chain counter %d out of range", counter)
	}
	var key []byte
	if nc.ChainKey.Key != nil {
		if len(nc.ChainKey.Key) != 32 {
			return nil, fmt.Errorf("chain key has %d bytes, want 32", len(nc.ChainKey.Key))
		}
		key = bytes.Clone(nc.ChainKey.Key)
	}
	return &chain.KeyStructure{Key: key, Index: uint32(counter + 1)}, nil
}

// convertMessageKeys expands libsignal-node's stored message key seeds
// (HMAC(chainKey, 0x01)) into the cipher key, MAC key and IV that
// go.mau.fi/libsignal stores, with the same HKDF libsignal-node runs at
// decryption time: HKDF-SHA256(seed, salt = 32 zero bytes,
// info = "WhisperMessageKeys") -> 80 bytes.
func convertMessageKeys(seeds map[string]Bytes) ([]*message.KeysStructure, int, error) {
	type indexed struct {
		index uint32
		seed  []byte
	}
	list := make([]indexed, 0, len(seeds))
	for k, seed := range seeds {
		idx, err := strconv.ParseUint(k, 10, 32)
		if err != nil {
			return nil, 0, fmt.Errorf("message key index %q: %w", k, err)
		}
		if len(seed) != 32 {
			return nil, 0, fmt.Errorf("message key %d has %d bytes, want 32", idx, len(seed))
		}
		list = append(list, indexed{uint32(idx), seed})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].index < list[j].index })
	dropped := 0
	if len(list) > maxMessageKeysPerLink {
		dropped = len(list) - maxMessageKeysPerLink
		list = list[dropped:]
	}
	out := make([]*message.KeysStructure, 0, len(list))
	for _, item := range list {
		material, err := kdf.DeriveSecrets(item.seed, nil, []byte(message.KdfSalt), message.DerivedSecretsSize)
		if err != nil {
			return nil, 0, fmt.Errorf("derive message key %d: %w", item.index, err)
		}
		out = append(out, &message.KeysStructure{
			CipherKey: material[:message.CipherKeyLength],
			MacKey:    material[message.CipherKeyLength : message.CipherKeyLength+message.MacKeyLength],
			IV:        material[message.CipherKeyLength+message.MacKeyLength : message.DerivedSecretsSize],
			Index:     item.index,
		})
	}
	return out, dropped, nil
}

type orderedEntry struct {
	Key   string
	Value json.RawMessage
}

// decodeOrderedObject decodes a JSON object keeping member order, which
// libsignal-node relies on for _chains (insertion order = age).
func decodeOrderedObject(raw json.RawMessage) ([]orderedEntry, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected an object")
	}
	var out []orderedEntry
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected object key %v", keyTok)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		out = append(out, orderedEntry{Key: key, Value: value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}
