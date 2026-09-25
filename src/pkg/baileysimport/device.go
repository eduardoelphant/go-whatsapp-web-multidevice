package baileysimport

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"

	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
)

// ErrNotPaired is returned when creds do not describe a paired companion
// device (no creds.me or creds.account).
var ErrNotPaired = errors.New("creds do not describe a paired device (missing me.id or account)")

// maxSignalKeyID is the exclusive upper bound whatsmeow's schema accepts for
// pre-key and signed pre-key ids.
const maxSignalKeyID = 1 << 24

// DeviceSpec is the whatsmeow device row derived from Baileys creds.
type DeviceSpec struct {
	ID             types.JID
	LID            types.JID
	RegistrationID uint32
	NoiseKey       *keys.KeyPair
	IdentityKey    *keys.KeyPair
	SignedPreKey   *keys.PreKey
	AdvSecretKey   []byte
	Account        *waAdv.ADVSignedDeviceIdentity
	Platform       string
	PushName       string
}

// apply copies the spec onto a store.Device created by the target container.
func (d *DeviceSpec) apply(dev *store.Device) {
	id := d.ID
	dev.ID = &id
	dev.LID = d.LID
	dev.RegistrationID = d.RegistrationID
	dev.NoiseKey = d.NoiseKey
	dev.IdentityKey = d.IdentityKey
	dev.SignedPreKey = d.SignedPreKey
	dev.AdvSecretKey = d.AdvSecretKey
	dev.Account = d.Account
	dev.Platform = d.Platform
	dev.PushName = d.PushName
}

// convertDevice maps Baileys creds to a whatsmeow device and checks that the
// key material is self-consistent.
func convertDevice(c Creds) (*DeviceSpec, error) {
	if c.Me == nil || c.Me.ID == "" || c.Account == nil {
		return nil, ErrNotPaired
	}
	id, err := types.ParseJID(c.Me.ID)
	if err != nil {
		return nil, fmt.Errorf("creds.me.id: %w", err)
	}
	if id.Server != types.DefaultUserServer || id.User == "" || id.Device == 0 {
		return nil, fmt.Errorf("creds.me.id %q is not a companion device JID (<phone>:<device>@%s)", c.Me.ID, types.DefaultUserServer)
	}
	var lid types.JID
	if c.Me.LID != "" {
		lid, err = types.ParseJID(c.Me.LID)
		if err != nil {
			return nil, fmt.Errorf("creds.me.lid: %w", err)
		}
		if lid.Server != types.HiddenUserServer || lid.User == "" {
			return nil, fmt.Errorf("creds.me.lid %q is not a LID", c.Me.LID)
		}
	}
	if c.RegistrationID == nil {
		return nil, fmt.Errorf("creds.registrationId is missing")
	}

	noise, err := keyPairFromBaileys("noiseKey", c.NoiseKey)
	if err != nil {
		return nil, err
	}
	identity, err := keyPairFromBaileys("signedIdentityKey", c.SignedIdentityKey)
	if err != nil {
		return nil, err
	}
	spkPair, err := keyPairFromBaileys("signedPreKey.keyPair", c.SignedPreKey.KeyPair)
	if err != nil {
		return nil, err
	}
	if c.SignedPreKey.KeyID >= maxSignalKeyID {
		return nil, fmt.Errorf("signedPreKey.keyId %d out of range", c.SignedPreKey.KeyID)
	}
	if len(c.SignedPreKey.Signature) != 64 {
		return nil, fmt.Errorf("signedPreKey.signature has %d bytes, want 64", len(c.SignedPreKey.Signature))
	}
	signature := [64]byte(c.SignedPreKey.Signature)
	if !ecc.VerifySignature(ecc.NewDjbECPublicKey(*identity.Pub), prefixedPublicKey(spkPair.Pub[:]), signature) {
		return nil, fmt.Errorf("signedPreKey.signature does not verify against signedIdentityKey")
	}

	adv, err := decodeBase64(c.AdvSecretKey)
	if err != nil || len(adv) == 0 {
		return nil, fmt.Errorf("creds.advSecretKey is not valid base64")
	}

	account, err := convertAccount(c.Account)
	if err != nil {
		return nil, err
	}

	return &DeviceSpec{
		ID:             id,
		LID:            lid,
		RegistrationID: *c.RegistrationID,
		NoiseKey:       noise,
		IdentityKey:    identity,
		SignedPreKey:   &keys.PreKey{KeyPair: *spkPair, KeyID: c.SignedPreKey.KeyID, Signature: &signature},
		AdvSecretKey:   adv,
		Account:        account,
		Platform:       c.Platform,
		PushName:       c.Me.Name,
	}, nil
}

func convertAccount(a *SignedIdentity) (*waAdv.ADVSignedDeviceIdentity, error) {
	if len(a.Details) == 0 {
		return nil, fmt.Errorf("creds.account.details is empty")
	}
	sigKey, err := unprefixedPublicKey(a.AccountSignatureKey)
	if err != nil {
		return nil, fmt.Errorf("creds.account.accountSignatureKey: %w", err)
	}
	if len(a.AccountSignature) != 64 {
		return nil, fmt.Errorf("creds.account.accountSignature has %d bytes, want 64", len(a.AccountSignature))
	}
	if len(a.DeviceSignature) != 64 {
		return nil, fmt.Errorf("creds.account.deviceSignature has %d bytes, want 64", len(a.DeviceSignature))
	}
	return &waAdv.ADVSignedDeviceIdentity{
		Details:             bytes.Clone(a.Details),
		AccountSignatureKey: sigKey,
		AccountSignature:    bytes.Clone(a.AccountSignature),
		DeviceSignature:     bytes.Clone(a.DeviceSignature),
	}, nil
}

// keyPairFromBaileys rebuilds a whatsmeow key pair from the private key and
// checks that the derived public key equals the one Baileys stored.
// libsignal-node keeps private keys as generated (not necessarily clamped);
// curve25519.ScalarBaseMult and whatsmeow's signing clamp at use, so the
// private key is stored unchanged.
func keyPairFromBaileys(field string, kp KeyPair) (*keys.KeyPair, error) {
	priv, err := privateKey(kp.Private)
	if err != nil {
		return nil, fmt.Errorf("%s.private: %w", field, err)
	}
	pair := keys.NewKeyPairFromPrivateKey(priv)
	if len(kp.Public) > 0 {
		pub, err := unprefixedPublicKey(kp.Public)
		if err != nil {
			return nil, fmt.Errorf("%s.public: %w", field, err)
		}
		if subtle.ConstantTimeCompare(pub, pair.Pub[:]) != 1 {
			return nil, fmt.Errorf("%s: public key does not match the private key", field)
		}
	}
	return pair, nil
}

func privateKey(b []byte) ([32]byte, error) {
	if len(b) != 32 {
		return [32]byte{}, fmt.Errorf("private key has %d bytes, want 32", len(b))
	}
	return [32]byte(b), nil
}

// unprefixedPublicKey returns a 32-byte Curve25519 public key, dropping the
// 0x05 (DjbType) prefix libsignal adds.
func unprefixedPublicKey(b []byte) ([]byte, error) {
	switch {
	case len(b) == 32:
		return bytes.Clone(b), nil
	case len(b) == 33 && b[0] == ecc.DjbType:
		return bytes.Clone(b[1:]), nil
	default:
		return nil, fmt.Errorf("public key has %d bytes, want 32 or 33 with 0x05 prefix", len(b))
	}
}

// prefixedPublicKey returns the 33-byte serialized form (0x05 || key).
func prefixedPublicKey(b []byte) []byte {
	if len(b) == 33 && b[0] == ecc.DjbType {
		return bytes.Clone(b)
	}
	out := make([]byte, 0, 33)
	out = append(out, ecc.DjbType)
	return append(out, b...)
}

// normalizePublicKey validates a public key and returns its 33-byte form.
func normalizePublicKey(b []byte) ([]byte, error) {
	pub, err := unprefixedPublicKey(b)
	if err != nil {
		return nil, err
	}
	return prefixedPublicKey(pub), nil
}
