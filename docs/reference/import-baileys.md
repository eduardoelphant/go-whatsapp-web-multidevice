# Importing a Baileys session

`import-baileys` moves a WhatsApp companion session that was paired with
[Baileys](https://github.com/WhiskeySockets/Baileys) (v7, tested against
`v7.0.0-rc.9`) into the whatsmeow store GOWA uses, so the number keeps working
here without scanning a new QR code.

The conversion lives in `src/pkg/baileysimport` (a library with no dependency
on GOWA's application layers); the command is `src/cmd/import_baileys.go`.

## Read this first

- **Never run Baileys and GOWA with the same credentials.** Both would connect
  as the same companion device. The server keeps only one connection, the
  Signal ratchets of the two copies diverge, and messages start failing to
  decrypt on both sides. Stop the Baileys client for good before exporting.
- **Never log out on the Baileys side.** Logging out (from Baileys or from the
  phone's "Linked devices" list) unregisters the companion. The imported
  credentials die with it.
- **Export after the Baileys process has stopped.** Every message Baileys
  decrypts after the export advances the ratchet, and the imported copy would
  be stale.
- The dump contains every private key of the session. Treat it like a
  password, and delete it (and the old Baileys auth folder or rows) once the
  import is confirmed.

## Input format

One JSON document:

```json
{
  "creds": { "...": "Baileys AuthenticationCreds" },
  "keys": {
    "pre-key-1": { "public": "...", "private": "..." },
    "session-5511999999999.0": { "_sessions": { "...": "..." }, "version": "v1" },
    "session-123456789_1.3": { "...": "..." },
    "sender-key-120363000000000000@g.us::5511999999999::0": { "type": "Buffer", "data": "..." },
    "app-state-sync-key-AAAAAAbc": { "keyData": "...", "fingerprint": { }, "timestamp": 0 },
    "lid-mapping-5511999999999": "123456789",
    "lid-mapping-123456789_reverse": "5511999999999",
    "tctoken-123456789@lid": { "token": "...", "timestamp": "1758000000" }
  }
}
```

Key names are `<type>-<id>`, the naming Baileys' `SignalKeyStore` and
`useMultiFileAuthState` use. Values are what the store persisted:

- objects, or the same JSON stored as a string (double encoded);
- byte fields in any BufferJSON shape: `{"type":"Buffer","data":"<base64>"}`,
  `{"type":"Buffer","data":[1,2,3]}` or a Uint8Array object `{"0":1,"1":2}`;
- the `useMultiFileAuthState` file-name forms are accepted too: `--` instead of
  `::` in sender key ids and `__` instead of `/` in app state key ids.

A `useMultiFileAuthState` folder can be turned into this document with a few
lines of Node (file name without `.json` becomes the key):

```js
// node dump-auth.mjs <auth-folder> > baileys-auth.json
import fs from 'node:fs'
import path from 'node:path'
const dir = process.argv[2]
const read = f => JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'))
const keys = {}
for (const f of fs.readdirSync(dir)) {
	if (f.endsWith('.json') && f !== 'creds.json') keys[f.slice(0, -5)] = read(f)
}
process.stdout.write(JSON.stringify({ creds: read('creds.json'), keys }))
```

Stores that keep the auth state in a database only need to emit the same
`creds` object and `<type>-<id>` map.

## What is imported

| Baileys | whatsmeow | Notes |
|---|---|---|
| `creds.noiseKey`, `signedIdentityKey`, `signedPreKey`, `registrationId`, `advSecretKey` | device row | Public keys are re-derived from the private keys and must match; the signed pre-key signature is verified. Private keys are stored as-is (libsignal-node does not clamp; Curve25519 clamps at use). |
| `creds.account` | `adv_*` columns | Stored whole, `accountSignatureKey` included, as whatsmeow itself stores it after pairing. |
| `creds.me.id` / `me.lid` / `me.name`, `creds.platform` | JID, LID, push name, platform | `me.id` must be a companion JID (`<phone>:<device>@s.whatsapp.net`). |
| `pre-key-<id>` | `whatsmeow_pre_keys` | Original ids kept; `uploaded = id < creds.firstUnuploadedPreKeyId`. |
| `session-<name>.<device>` | `whatsmeow_sessions` + `whatsmeow_identity_keys` | Address `<name>.<device>` becomes `<name>:<device>` (`123_1.3` becomes `123_1:3`). See below. |
| `sender-key-<group>::<user>::<device>` | `whatsmeow_sender_keys` | `sender_id` is `<user>:<device>`. State order is reversed (Baileys appends, libsignal-go prepends). |
| `app-state-sync-key-<id>` | `whatsmeow_app_state_sync_keys` | Fingerprint re-encoded as protobuf; `timestamp` may be a number, a string or a long.js `Long`. |
| `lid-mapping-*` | `whatsmeow_lid_map` | Forward and `_reverse` entries are merged. |
| `tctoken-<jid>` | `whatsmeow_privacy_tokens` | Tokens without a timestamp are skipped (whatsmeow ages tokens by it). |

### Signal sessions

libsignal-node keeps a record of up to 40 sessions per address; whatsmeow keeps
go.mau.fi/libsignal records serialized as JSON. The conversion:

- the open session (`indexInfo.closed === -1`) becomes the current state; the
  closed ones become previous states, most recently used first (the order
  libsignal-node tries them in), at most 40;
- the sending chain is the one keyed by `currentRatchet.ephemeralKeyPair`,
  whose key pair becomes the sender ratchet key;
- receiving chains keep their order; go.mau.fi/libsignal holds five, so older
  ones are dropped (libsignal-node closes them but never deletes them);
- chain counters are shifted: libsignal-node's `counter` is the last index it
  derived, go.mau.fi/libsignal's `index` is the next one (`index = counter + 1`);
  a closed chain has no chain key and only serves its stored message keys;
- stored message keys are seeds in libsignal-node and are expanded with the
  same HKDF it runs at decryption time (salt of 32 zero bytes, info
  `WhisperMessageKeys`, 80 bytes: cipher key, MAC key, IV);
- `indexInfo.baseKey` becomes the Alice base key, `pendingPreKey` the pending
  pre-key, and the local identity is `0x05 || signedIdentityKey.public`;
- a record with no open session is skipped: Baileys would fetch a new pre-key
  bundle for it, while in whatsmeow the mere presence of a row prevents that.

## What is not imported, and why

- `app-state-sync-version-*` (and therefore the LTHash state and mutation
  MACs). Starting from version 0 makes whatsmeow fetch full snapshots of every
  collection. That is on purpose: the snapshot carries the `nct_salt_sync`
  mutation, and whatsmeow needs that salt for cs tokens while Baileys
  `v7.0.0-rc.9` never stores it. Resuming from Baileys' version would skip it.
- `creds.pairingEphemeralKeyPair` (pairing only), `processedHistoryMessages`,
  `routingInfo`, `accountSyncCounter`, `nextPreKeyId`, account settings and the
  other counters: whatsmeow does not keep them or keeps its own.
- `device-list-*` and `sender-key-memory-*`: caches; whatsmeow queries device
  lists and redistributes sender keys itself.
- Contacts, chat settings and message history are not part of a Baileys auth
  state. whatsmeow fills contacts from push names as messages arrive.

## Step by step

1. Stop the Baileys client and make sure nothing restarts it. Do not log out.
2. Export the auth state to `baileys-auth.json` (see above).
3. Stop GOWA and back up its store (`--db-uri`, and `--db-keys-uri` if used).
4. Validate without writing anything (prints identifiers and counts only):

   ```sh
   ./whatsapp import-baileys --input baileys-auth.json \
     --db-uri="file:storages/whatsapp.db" --dry-run
   ```

5. Import:

   ```sh
   ./whatsapp import-baileys --input baileys-auth.json --db-uri="file:storages/whatsapp.db"
   ```

   Flags: `--db-uri` / `--db-keys-uri` are the usual GOWA flags (or
   `DB_URI` / `DB_KEYS_URI`); with a keys database, identities, sessions,
   pre-keys and sender keys are written there, as GOWA expects.
   `--skip-signal-sessions` leaves 1:1 sessions out (peers re-establish them
   through retry receipts, at the cost of a few undecryptable messages).
   `--replace` overwrites a device with the same JID; without it the import
   refuses. The import runs in one transaction per database.
6. Start GOWA (`./whatsapp rest`). The device is discovered from the store. If
   the command warned that another companion of the same number is already in
   the store, remove the stale one first: GOWA adopts one companion per number.
7. Check that messages are received and sent in both directions, then delete
   the dump and the old Baileys credentials.

## What only a real session will tell

The tests prove the conversion against libsignal-node itself (below), but not
the server side: that WhatsApp accepts the imported device on login, how
quickly the app state snapshot resyncs, and how peers with trimmed or skipped
sessions recover. Try it first on a number you can re-pair.

## Regenerating test fixtures

The fixtures in `src/pkg/baileysimport/testdata` are synthetic (every key is
generated by the script) and versioned. `testdata/gen/gen_fixtures.mjs` builds
them with the real libsignal-node and Baileys' own Signal group code: it
establishes sessions, exchanges messages in both directions (out of order,
across several ratchet steps, with a re-established session and a pending
pre-key), and records ciphertexts the Go side must decrypt plus replies whose
bytes the Go side must reproduce. The Go tests do not need Node.

To regenerate (Node 22.18+; nothing is added to the Go module):

```sh
mkdir /tmp/baileysimport-deps && cd /tmp/baileysimport-deps
npm init -y >/dev/null
npm install --install-links github:WhiskeySockets/libsignal-node#e81ecfc protobufjs@7
git clone --depth 1 --branch v7.0.0-rc.9 https://github.com/WhiskeySockets/Baileys /tmp/baileys
cd -
BAILEYSIMPORT_NODE_DEPS=/tmp/baileysimport-deps BAILEYSIMPORT_BAILEYS_SRC=/tmp/baileys \
  node src/pkg/baileysimport/testdata/gen/gen_fixtures.mjs
```

Then run `go test ./pkg/baileysimport/... ./cmd/...` from `src/`.
