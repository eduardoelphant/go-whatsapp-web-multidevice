package baileysimport

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// AuthState is a Baileys authentication state dump:
//
//	{"creds": <AuthenticationCreds>, "keys": {"<type>-<id>": <value>, ...}}
//
// Key names follow Baileys' SignalKeyStore naming, the same one
// useMultiFileAuthState uses for its file names ("pre-key-1",
// "session-5511999999999.0", "sender-key-<group>::<user>::<device>", ...).
// Values may be objects or JSON documents stored as strings.
type AuthState struct {
	Creds Creds
	Keys  map[string]json.RawMessage
}

// Creds holds the fields of Baileys' AuthenticationCreds the importer uses.
// Everything else in creds (pairingEphemeralKeyPair, processedHistoryMessages,
// routingInfo, counters, account settings) is ignored on purpose.
type Creds struct {
	NoiseKey                KeyPair         `json:"noiseKey"`
	SignedIdentityKey       KeyPair         `json:"signedIdentityKey"`
	SignedPreKey            SignedKeyPair   `json:"signedPreKey"`
	RegistrationID          *uint32         `json:"registrationId"`
	AdvSecretKey            string          `json:"advSecretKey"`
	Me                      *Contact        `json:"me"`
	Account                 *SignedIdentity `json:"account"`
	Platform                string          `json:"platform"`
	FirstUnuploadedPreKeyID uint32          `json:"firstUnuploadedPreKeyId"`
	NextPreKeyID            uint32          `json:"nextPreKeyId"`
}

// KeyPair is Baileys' KeyPair: a 32-byte public key without the 0x05 type
// prefix and a 32-byte private key.
type KeyPair struct {
	Public  Bytes `json:"public"`
	Private Bytes `json:"private"`
}

// SignedKeyPair is Baileys' SignedKeyPair.
type SignedKeyPair struct {
	KeyPair   KeyPair `json:"keyPair"`
	Signature Bytes   `json:"signature"`
	KeyID     uint32  `json:"keyId"`
}

// Contact is the subset of Baileys' Contact stored as creds.me.
type Contact struct {
	ID   string `json:"id"`
	LID  string `json:"lid"`
	Name string `json:"name"`
}

// SignedIdentity is proto.IADVSignedDeviceIdentity as serialized by BufferJSON.
type SignedIdentity struct {
	Details             Bytes `json:"details"`
	AccountSignatureKey Bytes `json:"accountSignatureKey"`
	AccountSignature    Bytes `json:"accountSignature"`
	DeviceSignature     Bytes `json:"deviceSignature"`
}

// Parse reads a Baileys auth state dump. The creds document and the keys map
// may themselves be JSON strings (double encoded).
func Parse(r io.Reader) (*AuthState, error) {
	var top struct {
		Creds json.RawMessage `json:"creds"`
		Keys  json.RawMessage `json:"keys"`
	}
	dec := json.NewDecoder(r)
	if err := dec.Decode(&top); err != nil {
		return nil, fmt.Errorf("decode auth state: %w", err)
	}
	if len(top.Creds) == 0 || string(top.Creds) == "null" {
		return nil, fmt.Errorf("auth state has no creds")
	}
	credsRaw, err := unwrapJSON(top.Creds, false)
	if err != nil {
		return nil, fmt.Errorf("creds: %w", err)
	}
	state := &AuthState{Keys: map[string]json.RawMessage{}}
	if err := json.Unmarshal(credsRaw, &state.Creds); err != nil {
		return nil, fmt.Errorf("creds: %w", err)
	}
	if len(top.Keys) > 0 && string(top.Keys) != "null" {
		keysRaw, err := unwrapJSON(top.Keys, false)
		if err != nil {
			return nil, fmt.Errorf("keys: %w", err)
		}
		if err := json.Unmarshal(keysRaw, &state.Keys); err != nil {
			return nil, fmt.Errorf("keys: %w", err)
		}
	}
	return state, nil
}

// Key types of Baileys' SignalDataTypeMap.
const (
	KeyTypePreKey              = "pre-key"
	KeyTypeSession             = "session"
	KeyTypeSenderKey           = "sender-key"
	KeyTypeSenderKeyMemory     = "sender-key-memory"
	KeyTypeAppStateSyncKey     = "app-state-sync-key"
	KeyTypeAppStateSyncVersion = "app-state-sync-version"
	KeyTypeLIDMapping          = "lid-mapping"
	KeyTypeDeviceList          = "device-list"
	KeyTypeTCToken             = "tctoken"
	keyTypeUnknown             = "unknown"
)

// knownKeyTypes is ordered so that a type that is a prefix of another type
// ("sender-key" / "sender-key-memory", "app-state-sync-key" /
// "app-state-sync-version") is tried after the longer one.
var knownKeyTypes = []string{
	KeyTypeAppStateSyncVersion,
	KeyTypeAppStateSyncKey,
	KeyTypeSenderKeyMemory,
	KeyTypeSenderKey,
	KeyTypeLIDMapping,
	KeyTypeDeviceList,
	KeyTypePreKey,
	KeyTypeSession,
	KeyTypeTCToken,
}

// splitKeyName splits "<type>-<id>" into its type and id. Unknown names return
// keyTypeUnknown and the full name.
func splitKeyName(name string) (keyType, id string) {
	for _, t := range knownKeyTypes {
		if strings.HasPrefix(name, t+"-") && len(name) > len(t)+1 {
			return t, name[len(t)+1:]
		}
	}
	return keyTypeUnknown, name
}
