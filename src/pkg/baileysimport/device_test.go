package baileysimport

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/util/keys"
)

func loadCreds(t *testing.T) Creds {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "bob_auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	state, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return state.Creds
}

func TestConvertDeviceMapsCreds(t *testing.T) {
	c := loadCreds(t)
	d, err := convertDevice(c)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID.String() != c.Me.ID || d.LID.String() != c.Me.LID || d.PushName != c.Me.Name || d.Platform != c.Platform {
		t.Fatalf("identity fields: %s %s %q %q", d.ID, d.LID, d.PushName, d.Platform)
	}
	if d.RegistrationID != *c.RegistrationID {
		t.Fatalf("registration id %d", d.RegistrationID)
	}
	// Public keys derived by Go from the Node private keys equal the 32-byte
	// public keys Baileys stored (without the 0x05 prefix).
	for name, pair := range map[string]struct {
		got  *keys.KeyPair
		want KeyPair
	}{
		"noise":    {d.NoiseKey, c.NoiseKey},
		"identity": {d.IdentityKey, c.SignedIdentityKey},
		"signed":   {&d.SignedPreKey.KeyPair, c.SignedPreKey.KeyPair},
	} {
		if !bytes.Equal(pair.got.Pub[:], pair.want.Public) || !bytes.Equal(pair.got.Priv[:], pair.want.Private) {
			t.Errorf("%s key pair differs", name)
		}
	}
	if d.SignedPreKey.KeyID != c.SignedPreKey.KeyID || !bytes.Equal(d.SignedPreKey.Signature[:], c.SignedPreKey.Signature) {
		t.Error("signed pre-key id/signature differ")
	}
	adv, _ := decodeBase64(c.AdvSecretKey)
	if !bytes.Equal(d.AdvSecretKey, adv) || len(adv) != 32 {
		t.Error("adv secret differs")
	}
	if !bytes.Equal(d.Account.Details, c.Account.Details) || !bytes.Equal(d.Account.AccountSignatureKey, c.Account.AccountSignatureKey) ||
		!bytes.Equal(d.Account.AccountSignature, c.Account.AccountSignature) || !bytes.Equal(d.Account.DeviceSignature, c.Account.DeviceSignature) {
		t.Error("account differs")
	}
}

// libsignal-node does not clamp private keys; Go clamps at use. An unclamped
// key must be accepted, stored unchanged, and yield the same public key.
func TestKeyPairFromBaileysUnclampedPrivateKey(t *testing.T) {
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	priv[0] |= 0x07
	priv[31] |= 0x80
	priv[31] &^= 0x40
	// crypto/ecdh clamps like RFC 7748, independently of whatsmeow.
	ecdhKey, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub := ecdhKey.PublicKey().Bytes()
	pair, err := keyPairFromBaileys("k", KeyPair{Public: pub, Private: priv})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pair.Priv[:], priv) || !bytes.Equal(pair.Pub[:], pub) {
		t.Fatal("unclamped key not preserved")
	}
	// The 33-byte form with the 0x05 prefix is accepted too.
	if _, err := keyPairFromBaileys("k", KeyPair{Public: append([]byte{5}, pub...), Private: priv}); err != nil {
		t.Fatal(err)
	}
	other := bytes.Clone(pub)
	other[0] ^= 1
	if _, err := keyPairFromBaileys("k", KeyPair{Public: other, Private: priv}); err == nil {
		t.Fatal("mismatched public key accepted")
	}
}

func TestConvertDeviceRejects(t *testing.T) {
	base := loadCreds(t)
	clone := func() Creds {
		raw, _ := json.Marshal(base)
		var c Creds
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	cases := map[string]func(c *Creds){
		"not paired":        func(c *Creds) { c.Me = nil },
		"no account":        func(c *Creds) { c.Account = nil },
		"primary device id": func(c *Creds) { c.Me.ID = "5511900000001@s.whatsapp.net" },
		"lid as id":         func(c *Creds) { c.Me.ID = "100000000000001:7@lid" },
		"bad lid":           func(c *Creds) { c.Me.LID = "5511900000001:7@s.whatsapp.net" },
		"no registration":   func(c *Creds) { c.RegistrationID = nil },
		"identity mismatch": func(c *Creds) { c.SignedIdentityKey.Public = bytes.Clone(c.NoiseKey.Public) },
		"bad spk signature": func(c *Creds) { c.SignedPreKey.Signature[10] ^= 1 },
		"bad adv secret":    func(c *Creds) { c.AdvSecretKey = "!!" },
		"short device sig":  func(c *Creds) { c.Account.DeviceSignature = c.Account.DeviceSignature[:10] },
	}
	for name, mutate := range cases {
		c := clone()
		mutate(&c)
		_, err := convertDevice(c)
		if err == nil {
			t.Errorf("%s: expected error", name)
		}
		if name == "not paired" && !errors.Is(err, ErrNotPaired) {
			t.Errorf("not paired: got %v", err)
		}
	}
}
