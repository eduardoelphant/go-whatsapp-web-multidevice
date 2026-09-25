package baileysimport

import (
	"bytes"
	"encoding/json"
	"fmt"

	"go.mau.fi/libsignal/groups/ratchet"
	groupRecord "go.mau.fi/libsignal/groups/state/record"
	"go.mau.fi/whatsmeow/store"
)

const maxSenderKeyStates = 5

// nodeSenderKeyState is Baileys' SenderKeyStateStructure. Baileys persists a
// sender key record as a Buffer holding JSON.stringify(record.serialize()),
// where the inner byte fields use Node's default Buffer/Uint8Array JSON forms.
type nodeSenderKeyState struct {
	SenderKeyID    uint32 `json:"senderKeyId"`
	SenderChainKey struct {
		Iteration uint32 `json:"iteration"`
		Seed      Bytes  `json:"seed"`
	} `json:"senderChainKey"`
	SenderSigningKey struct {
		Public  Bytes `json:"public"`
		Private Bytes `json:"private"`
	} `json:"senderSigningKey"`
	SenderMessageKeys []struct {
		Iteration uint32 `json:"iteration"`
		Seed      Bytes  `json:"seed"`
	} `json:"senderMessageKeys"`
}

// convertSenderKeyRecord turns a Baileys sender key record into the
// serialized go.mau.fi/libsignal SenderKey record. Baileys appends new states
// (newest last) while go.mau.fi/libsignal prepends them (newest first, and
// state 0 is the one used to encrypt), so the order is reversed.
func convertSenderKeyRecord(raw json.RawMessage) ([]byte, int, error) {
	var states []nodeSenderKeyState
	if err := json.Unmarshal(raw, &states); err != nil {
		return nil, 0, fmt.Errorf("invalid sender key record: %w", err)
	}
	if len(states) == 0 {
		return nil, 0, fmt.Errorf("sender key record has no states")
	}
	dropped := 0
	if len(states) > maxSenderKeyStates {
		states = states[len(states)-maxSenderKeyStates:]
	}
	structure := &groupRecord.SenderKeyStructure{}
	for i := len(states) - 1; i >= 0; i-- {
		st, d, err := convertSenderKeyState(states[i])
		if err != nil {
			return nil, 0, fmt.Errorf("sender key state %d: %w", states[i].SenderKeyID, err)
		}
		dropped += d
		structure.SenderKeyStates = append(structure.SenderKeyStates, st)
	}
	serializer := store.SignalProtobufSerializer
	serialized := serializer.SenderKeyRecord.Serialize(structure)
	if _, err := groupRecord.NewSenderKeyFromBytes(serialized, serializer.SenderKeyRecord, serializer.SenderKeyState); err != nil {
		return nil, 0, fmt.Errorf("converted sender key does not load: %w", err)
	}
	return serialized, dropped, nil
}

func convertSenderKeyState(s nodeSenderKeyState) (*groupRecord.SenderKeyStateStructure, int, error) {
	if len(s.SenderChainKey.Seed) != 32 {
		return nil, 0, fmt.Errorf("chain key seed has %d bytes, want 32", len(s.SenderChainKey.Seed))
	}
	// Baileys' getSigningKeyPublic adds the 0x05 prefix to 32-byte keys.
	signingPublic, err := normalizePublicKey(s.SenderSigningKey.Public)
	if err != nil {
		return nil, 0, fmt.Errorf("signing public key: %w", err)
	}
	var signingPrivate []byte
	switch len(s.SenderSigningKey.Private) {
	case 0:
	case 32:
		signingPrivate = bytes.Clone(s.SenderSigningKey.Private)
	default:
		return nil, 0, fmt.Errorf("signing private key has %d bytes, want 32", len(s.SenderSigningKey.Private))
	}
	msgKeys := s.SenderMessageKeys
	dropped := 0
	if len(msgKeys) > maxMessageKeysPerLink {
		dropped = len(msgKeys) - maxMessageKeysPerLink
		msgKeys = msgKeys[dropped:]
	}
	keys := make([]*ratchet.SenderMessageKeyStructure, 0, len(msgKeys))
	for _, mk := range msgKeys {
		if len(mk.Seed) != 32 {
			return nil, 0, fmt.Errorf("message key %d seed has %d bytes, want 32", mk.Iteration, len(mk.Seed))
		}
		// Same derivation on both sides: HKDF(seed, "WhisperGroup") -> iv(16) || cipherKey(32).
		derived, err := ratchet.NewSenderMessageKey(mk.Iteration, bytes.Clone(mk.Seed))
		if err != nil {
			return nil, 0, err
		}
		keys = append(keys, ratchet.NewStructFromSenderMessageKey(derived))
	}
	return &groupRecord.SenderKeyStateStructure{
		Keys:  keys,
		KeyID: s.SenderKeyID,
		SenderChainKey: &ratchet.SenderChainKeyStructure{
			Iteration: s.SenderChainKey.Iteration,
			ChainKey:  bytes.Clone(s.SenderChainKey.Seed),
		},
		SigningKeyPrivate: signingPrivate,
		SigningKeyPublic:  signingPublic,
	}, dropped, nil
}
