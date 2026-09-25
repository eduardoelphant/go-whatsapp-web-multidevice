// Fixture generator for the baileysimport round-trip tests.
//
// It drives the real libsignal-node (the one Baileys v7.0.0-rc.9 depends on)
// and Baileys' own Signal group code to build synthetic Baileys auth states,
// then writes them next to this directory together with ciphertexts the Go
// tests must decrypt and replies whose bytes the Go tests must reproduce.
// Every key is generated here; nothing touches WhatsApp.
//
// This is a development tool, not a runtime dependency of the Go module.
// Requirements (see docs/import-baileys.md, "Regenerating test fixtures"):
//
//   BAILEYSIMPORT_NODE_DEPS   directory whose node_modules holds
//                             @whiskeysockets/libsignal-node (commit e81ecfc)
//                             and protobufjs@7
//   BAILEYSIMPORT_BAILEYS_SRC checkout of Baileys v7.0.0-rc.9
//
//   node gen_fixtures.mjs
//
// Node 22.18+ is required (TypeScript type stripping and module.registerHooks).

import crypto from 'node:crypto'
import fs from 'node:fs'
import { createRequire, registerHooks } from 'node:module'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { BufferJSON } from './bufferjson.mjs'

const here = path.dirname(fileURLToPath(import.meta.url))
const outDir = path.resolve(here, '..')
const depsDir = process.env.BAILEYSIMPORT_NODE_DEPS
const baileysDir = process.env.BAILEYSIMPORT_BAILEYS_SRC
if (!depsDir || !baileysDir) {
	console.error('set BAILEYSIMPORT_NODE_DEPS and BAILEYSIMPORT_BAILEYS_SRC')
	process.exit(2)
}

const depsRequire = createRequire(path.join(path.resolve(depsDir), 'noop.js'))
const shimURL = pathToFileURL(path.join(here, 'bufferjson.mjs')).href
const toURL = file => pathToFileURL(file).href

// Baileys sources import extensionless paths, the "libsignal" git dependency
// and its own utilities. Resolve them to the installed packages and to the
// BufferJSON copy above.
registerHooks({
	resolve(specifier, context, nextResolve) {
		if (specifier.endsWith('/Utils/generics')) {
			return { url: shimURL, shortCircuit: true }
		}
		if (specifier.startsWith('libsignal/')) {
			const target = '@whiskeysockets/libsignal-node/' + specifier.slice('libsignal/'.length)
			return { url: toURL(depsRequire.resolve(target)), shortCircuit: true }
		}
		try {
			return nextResolve(specifier, context)
		} catch (err) {
			if (specifier.startsWith('.')) {
				return nextResolve(specifier + '.ts', context)
			}
			return { url: toURL(depsRequire.resolve(specifier)), shortCircuit: true }
		}
	}
})

const libsignal = depsRequire('@whiskeysockets/libsignal-node')
const { curve, keyhelper, ProtocolAddress, SessionBuilder, SessionCipher, SessionRecord } = libsignal
const group = await import(toURL(path.join(baileysDir, 'src/Signal/Group/index.ts')))
const { GroupCipher, GroupSessionBuilder, SenderKeyDistributionMessage, SenderKeyName, SenderKeyRecord } = group

const pub33 = pub => Buffer.concat([Buffer.from([5]), Buffer.from(pub)])

// Baileys' Curve.generateKeyPair: 32-byte public key without the 0x05 prefix.
function newKeyPair() {
	const { pubKey, privKey } = curve.generateKeyPair()
	return { public: Buffer.from(pubKey.slice(1)), private: Buffer.from(privKey) }
}

// Baileys' signedKeyPair.
function signedKeyPair(identity, keyId) {
	const preKey = newKeyPair()
	const signature = curve.calculateSignature(identity.private, pub33(preKey.public))
	return { keyPair: preKey, signature, keyId }
}

const persist = value => JSON.stringify(value, BufferJSON.replacer)
const load = text => JSON.parse(text, BufferJSON.reviver)

// Party mirrors a Baileys client: creds plus a flat "<type>-<id>" key store
// persisted exactly like useMultiFileAuthState (BufferJSON replacer/reviver).
class Party {
	constructor({ user, device, lid, name, preKeys = 0 }) {
		this.address = `${user}.${device}`
		const identity = newKeyPair()
		this.creds = {
			noiseKey: newKeyPair(),
			pairingEphemeralKeyPair: newKeyPair(),
			signedIdentityKey: identity,
			signedPreKey: signedKeyPair(identity, 1),
			registrationId: keyhelper.generateRegistrationId(),
			advSecretKey: crypto.randomBytes(32).toString('base64'),
			processedHistoryMessages: [{ key: { remoteJid: `${user}@s.whatsapp.net`, id: 'ABCDEF' }, messageTimestamp: 1758000000 }],
			nextPreKeyId: preKeys + 1,
			firstUnuploadedPreKeyId: Math.min(21, preKeys + 1),
			accountSyncCounter: 1,
			accountSettings: { unarchiveChats: false },
			registered: false,
			pairingCode: undefined,
			lastPropHash: 'synthetic',
			routingInfo: crypto.randomBytes(8),
			me: { id: `${user}:${device}@s.whatsapp.net`, lid: `${lid}:${device}@lid`, name },
			account: {
				details: crypto.randomBytes(40),
				accountSignatureKey: crypto.randomBytes(32),
				accountSignature: crypto.randomBytes(64),
				deviceSignature: crypto.randomBytes(64)
			},
			signalIdentities: [{ identifier: { name: `${lid}:0@lid`, deviceId: 0 }, identifierKey: pub33(crypto.randomBytes(32)) }],
			myAppStateKeyId: 'AAAAAA==',
			platform: 'android'
		}
		this.keys = new Map()
		for (let id = 1; id <= preKeys; id++) {
			this.set('pre-key', id, newKeyPair())
		}
	}

	get(type, id) {
		const raw = this.keys.get(`${type}-${id}`)
		return raw === undefined ? undefined : load(raw)
	}

	set(type, id, value) {
		if (value === null || value === undefined) {
			this.keys.delete(`${type}-${id}`)
		} else {
			this.keys.set(`${type}-${id}`, persist(value))
		}
	}

	clone() {
		const copy = Object.create(Party.prototype)
		copy.address = this.address
		copy.creds = load(persist(this.creds))
		copy.keys = new Map(this.keys)
		return copy
	}

	// Same shape as Baileys' signalStorage (src/Signal/libsignal.ts).
	storage() {
		return {
			loadSession: async id => {
				const sess = this.get('session', id)
				return sess ? SessionRecord.deserialize(sess) : null
			},
			storeSession: async (id, record) => this.set('session', id, record.serialize()),
			isTrustedIdentity: () => true,
			loadPreKey: async id => {
				const key = this.get('pre-key', id.toString())
				if (key) {
					return { privKey: Buffer.from(key.private), pubKey: Buffer.from(key.public) }
				}
			},
			removePreKey: id => this.set('pre-key', id, null),
			loadSignedPreKey: () => {
				const key = this.creds.signedPreKey
				return { privKey: Buffer.from(key.keyPair.private), pubKey: Buffer.from(key.keyPair.public) }
			},
			loadSenderKey: async name => {
				const key = this.get('sender-key', name.toString())
				return key ? SenderKeyRecord.deserialize(key) : new SenderKeyRecord()
			},
			storeSenderKey: async (name, record) => {
				this.set('sender-key', name.toString(), Buffer.from(JSON.stringify(record.serialize()), 'utf-8'))
			},
			getOurRegistrationId: () => this.creds.registrationId,
			getOurIdentity: () => ({
				privKey: Buffer.from(this.creds.signedIdentityKey.private),
				pubKey: pub33(this.creds.signedIdentityKey.public)
			})
		}
	}

	bundle(preKeyId) {
		const preKey = this.get('pre-key', preKeyId)
		const spk = this.creds.signedPreKey
		return {
			identityKey: pub33(this.creds.signedIdentityKey.public),
			registrationId: this.creds.registrationId,
			preKey: { keyId: preKeyId, publicKey: pub33(preKey.public) },
			signedPreKey: { keyId: spk.keyId, publicKey: pub33(spk.keyPair.public), signature: spk.signature }
		}
	}

	dump() {
		const keys = {}
		for (const [k, v] of [...this.keys.entries()].sort(([a], [b]) => a.localeCompare(b))) {
			keys[k] = JSON.parse(v)
		}
		return { creds: JSON.parse(persist(this.creds)), keys }
	}
}

async function initOutgoing(from, toAddr, bundle) {
	await new SessionBuilder(from.storage(), ProtocolAddress.from(toAddr)).initOutgoing(bundle)
}

async function encrypt(from, toAddr, text) {
	const res = await new SessionCipher(from.storage(), ProtocolAddress.from(toAddr)).encrypt(Buffer.from(text))
	return { type: res.type === 3 ? 'pkmsg' : 'msg', body: Buffer.from(res.body) }
}

async function decrypt(to, fromAddr, msg) {
	const cipher = new SessionCipher(to.storage(), ProtocolAddress.from(fromAddr))
	const out = msg.type === 'pkmsg' ? await cipher.decryptPreKeyWhisperMessage(msg.body) : await cipher.decryptWhisperMessage(msg.body)
	return Buffer.from(out).toString()
}

async function expectDecrypt(to, fromAddr, msg, text) {
	const got = await decrypt(to, fromAddr, msg)
	if (got !== text) {
		throw new Error(`${to.address} decrypted ${JSON.stringify(got)}, want ${JSON.stringify(text)}`)
	}
}

const vector = (from, msg, plaintext) => ({ from, type: msg.type, body: msg.body.toString('base64'), plaintext })

const bob = new Party({ user: '5511900000001', device: 7, lid: '100000000000001', name: 'Bob Import', preKeys: 30 })
const alice = new Party({ user: '5511900000002', device: 3, lid: '100000000000002', name: 'Alice Peer', preKeys: 10 })
const carol = new Party({ user: '5511900000003', device: 0, lid: '200000000000003', name: 'Carol' })
const dave = new Party({ user: '5511900000004', device: 0, lid: '100000000000004', name: 'Dave', preKeys: 10 })
const eve = new Party({ user: '5511900000005', device: 0, lid: '300000000000005', name: 'Eve' })

const BOB = bob.address
const ALICE = alice.address
const CAROL = '200000000000003_1.0' // Carol talks to Bob from her LID
const DAVE = dave.address
const EVE = '300000000000005_1.0'

// Alice <-> Bob: several ratchet steps, out-of-order delivery, a closed
// receiving chain with a stored message key, and a new chain Bob never saw.
await initOutgoing(alice, BOB, bob.bundle(3))
const a = {}
for (const n of [1, 2, 3]) a[n] = await encrypt(alice, BOB, `a${n}`)
await expectDecrypt(bob, ALICE, a[1], 'a1')
await expectDecrypt(bob, ALICE, a[3], 'a3')
for (const n of [1, 2]) await expectDecrypt(alice, BOB, await encrypt(bob, ALICE, `b${n}`), `b${n}`)
for (const n of [4, 5, 6, 7]) a[n] = await encrypt(alice, BOB, `a${n}`)
await expectDecrypt(bob, ALICE, a[4], 'a4')
await expectDecrypt(bob, ALICE, a[6], 'a6')
await expectDecrypt(alice, BOB, await encrypt(bob, ALICE, 'b3'), 'b3')
a[8] = await encrypt(alice, BOB, 'a8')

// Carol opens a brand-new session with a pre-key Bob has not consumed yet.
await initOutgoing(carol, BOB, bob.bundle(5))
const c1 = await encrypt(carol, BOB, 'c1')

// Bob opened a session with Dave, who has not answered: the record still
// carries pendingPreKey, so the next message must be a pkmsg again.
await initOutgoing(bob, DAVE, dave.bundle(9))
const d1 = await encrypt(bob, DAVE, 'd1')

// Eve re-established her session: Bob keeps the old one as a closed session
// and Eve's e2, sent on the old session, is still undelivered.
await initOutgoing(eve, BOB, bob.bundle(7))
await expectDecrypt(bob, EVE, await encrypt(eve, BOB, 'e1'), 'e1')
await expectDecrypt(eve, BOB, await encrypt(bob, EVE, 'f1'), 'f1')
const e2 = await encrypt(eve, BOB, 'e2')
await initOutgoing(eve, BOB, bob.bundle(8))
await expectDecrypt(bob, EVE, await encrypt(eve, BOB, 'e3'), 'e3')

// Group sender keys.
const GROUP = '120363000000000001@g.us'
const aliceSender = ProtocolAddress.from(ALICE)
const bobSender = ProtocolAddress.from(BOB)
const aliceKeyName = new SenderKeyName(GROUP, aliceSender)
const bobKeyName = new SenderKeyName(GROUP, bobSender)

async function distribute(owner, receiver, name) {
	const skdm = await new GroupSessionBuilder(owner.storage()).create(name)
	const received = new SenderKeyDistributionMessage(null, null, null, null, skdm.serialize())
	await new GroupSessionBuilder(receiver.storage()).process(name, received)
}

const groupEncrypt = async (owner, name, text) =>
	Buffer.from(await new GroupCipher(owner.storage(), name).encrypt(Buffer.from(text)))

async function groupExpect(receiver, name, body, text) {
	const got = Buffer.from(await new GroupCipher(receiver.storage(), name).decrypt(body)).toString()
	if (got !== text) {
		throw new Error(`group decrypt got ${JSON.stringify(got)}, want ${JSON.stringify(text)}`)
	}
}

await distribute(alice, bob, aliceKeyName)
const g = {}
for (const n of [1, 2, 3, 4]) g[n] = await groupEncrypt(alice, aliceKeyName, `g${n}`)
await groupExpect(bob, aliceKeyName, g[1], 'g1')
await groupExpect(bob, aliceKeyName, g[3], 'g3')
// Alice rotates her sender key: Bob's record now holds two states.
alice.set('sender-key', aliceKeyName.toString(), null)
await distribute(alice, bob, aliceKeyName)
g[5] = await groupEncrypt(alice, aliceKeyName, 'g5')

await distribute(bob, alice, bobKeyName)
for (const n of [1, 2]) await groupExpect(alice, bobKeyName, await groupEncrypt(bob, bobKeyName, `o${n}`), `o${n}`)

// Replies whose bytes the Go side must reproduce from the imported state.
// They are computed on clones so the exported states stay untouched, and
// the receiving clone proves Node accepts them.
const r1 = await encrypt(bob.clone(), ALICE, 'r1')
await expectDecrypt(alice.clone(), BOB, r1, 'r1')
const d2 = await encrypt(bob.clone(), DAVE, 'd2')
{
	const daveClone = dave.clone()
	await expectDecrypt(daveClone, BOB, d1, 'd1')
	await expectDecrypt(daveClone, BOB, d2, 'd2')
}

// Side data that is imported (LID map, tctokens, app state keys) or ignored.
bob.set('lid-mapping', '5511900000002', '100000000000002')
bob.set('lid-mapping', '100000000000002_reverse', '5511900000002')
bob.set('lid-mapping', '300000000000005_reverse', '5511900000005')
bob.set('tctoken', '100000000000002@lid', { token: crypto.randomBytes(16), timestamp: '1758000000' })
bob.set('tctoken', '5511900000004@s.whatsapp.net', { token: crypto.randomBytes(16) })
const appStateKeyA = Buffer.from([0xff, 0xff, 0xff, 0x01, 0x02, 0x03])
const appStateKeyB = Buffer.from([0x00, 0x00, 0x00, 0x00, 0x2a, 0x00])
bob.set('app-state-sync-key', appStateKeyA.toString('base64'), {
	keyData: crypto.randomBytes(32),
	fingerprint: { rawId: 123456, currentIndex: 2, deviceIndexes: [0, 7] },
	timestamp: { low: 1358376059, high: 409, unsigned: false }
})
bob.set('app-state-sync-key', appStateKeyB.toString('base64'), {
	keyData: crypto.randomBytes(32),
	fingerprint: { rawId: 654321, currentIndex: 1, deviceIndexes: [0] },
	timestamp: 1758000000999
})
bob.set('app-state-sync-version', 'regular_high', { version: 12, hash: crypto.randomBytes(128), indexValueMap: {} })
bob.set('device-list', '5511900000002', ['0', '3'])
bob.set('sender-key-memory', GROUP, { [`${ALICE}`]: true })

// Exercise the alternative encodings the importer accepts: a value stored as
// a JSON string, Buffers as byte arrays and as numeric-key objects, and the
// file-name form of an app-state-sync-key id ("/" written as "__").
const bobDump = bob.dump()
bobDump.keys[`session-${EVE}`] = JSON.stringify(bobDump.keys[`session-${EVE}`])
bobDump.keys['pre-key-2'] = JSON.stringify(bobDump.keys['pre-key-2'])
const tc = bobDump.keys['tctoken-100000000000002@lid']
tc.token = { type: 'Buffer', data: [...Buffer.from(tc.token.data, 'base64')] }
bobDump.creds.account.details = Object.fromEntries([...Buffer.from(bobDump.creds.account.details.data, 'base64')].map((b, i) => [String(i), b]))
const renamedKey = `app-state-sync-key-${appStateKeyA.toString('base64').replace(/\//g, '__')}`
bobDump.keys[renamedKey] = bobDump.keys[`app-state-sync-key-${appStateKeyA.toString('base64')}`]
delete bobDump.keys[`app-state-sync-key-${appStateKeyA.toString('base64')}`]

const vectors = {
	bob: { jid: bob.creds.me.id, lid: bob.creds.me.lid, address: '5511900000001:7' },
	alice: { jid: alice.creds.me.id, address: '5511900000002:3' },
	pairwise: [
		vector('5511900000002:3', a[2], 'a2'),
		vector('5511900000002:3', a[5], 'a5'),
		vector('5511900000002:3', a[8], 'a8'),
		vector('5511900000002:3', a[7], 'a7'),
		vector('200000000000003_1:0', c1, 'c1'),
		vector('300000000000005_1:0', e2, 'e2')
	],
	reply: { to: '5511900000002:3', plaintext: 'r1', type: r1.type, body: r1.body.toString('base64') },
	prekeyReply: { to: '5511900000004:0', plaintext: 'd2', type: d2.type, body: d2.body.toString('base64') },
	group: {
		id: GROUP,
		ownSender: '5511900000001:7',
		pending: [
			{ sender: '5511900000002:3', body: g[2].toString('base64'), plaintext: 'g2' },
			{ sender: '5511900000002:3', body: g[4].toString('base64'), plaintext: 'g4' },
			{ sender: '5511900000002:3', body: g[5].toString('base64'), plaintext: 'g5' }
		]
	},
	appStateKeyIds: [appStateKeyA.toString('base64'), appStateKeyB.toString('base64')]
}

fs.writeFileSync(path.join(outDir, 'bob_auth.json'), JSON.stringify(bobDump, null, 1) + '\n')
fs.writeFileSync(path.join(outDir, 'alice_auth.json'), JSON.stringify(alice.dump(), null, 1) + '\n')
fs.writeFileSync(path.join(outDir, 'vectors.json'), JSON.stringify(vectors, null, 1) + '\n')
console.log('fixtures written to', outDir)
