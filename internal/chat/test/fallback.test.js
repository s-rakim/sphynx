// The chat opened over plain http encrypts with web/vendor/webcrypto-fallback.js
// instead of the browser's crypto.subtle. Messages, files and security codes
// must work both ways between a browser using one and a browser using the other.
import assert from 'node:assert/strict';
import { test } from 'node:test';

const native = await import('../web/crypto.js?native');

// What crypto.js sees on an http page: getRandomValues, but no subtle.
const real = globalThis.crypto;
Object.defineProperty(globalThis, 'crypto', { value: { getRandomValues: (a) => real.getRandomValues(a) }, configurable: true });
const fallback = await import('../web/crypto.js?fallback');
Object.defineProperty(globalThis, 'crypto', { value: real, configurable: true });

const { subtle: shim } = await import('../web/vendor/webcrypto-fallback.js');

async function browser(lib, id, user) {
  const keys = await lib.generateDeviceKeys();
  return { id, user, keys, lib };
}

function rings(...browsers) {
  const dir = new Map(browsers.map((b) => [b.id, b]));
  for (const b of browsers) {
    b.ring = new b.lib.KeyRing({
      deviceId: b.id,
      privateKey: b.keys.privateKey,
      fetchPublicKeys: async (ids) => ids.map((i) => ({ id: i, publicKey: dir.get(i).keys.publicJwk, userId: dir.get(i).user })),
    });
  }
  return browsers.map((b) => ({ id: b.id, publicKey: b.keys.publicJwk, userId: b.user }));
}

test('the http page really runs without crypto.subtle', () => {
  assert.equal(native.nativeCrypto, true);
  assert.equal(fallback.nativeCrypto, false);
});

test('messages sealed on one kind of page open on the other', async () => {
  const phone = await browser(fallback, 'dev-phone', 'phone@dev');
  const laptop = await browser(native, 'dev-laptop', 'laptop@dev');
  const recipients = rings(phone, laptop);
  const aad = native.chatAad('chat1', 'msg1');
  const fromPhone = await phone.ring.seal({ type: 'text', text: 'sent over http' }, aad, recipients);
  assert.equal((await laptop.ring.open(fromPhone, aad, 'phone@dev')).text, 'sent over http');
  assert.equal((await phone.ring.open(fromPhone, aad, 'phone@dev')).text, 'sent over http', 'and on the sender itself');
  const fromLaptop = await laptop.ring.seal({ type: 'text', text: 'sent over https' }, aad, recipients);
  assert.equal((await phone.ring.open(fromLaptop, aad, 'laptop@dev')).text, 'sent over https');
  await assert.rejects(phone.ring.open(fromLaptop, native.chatAad('chat1', 'other'), 'laptop@dev'), 'bound to its message id');
  await assert.rejects(phone.ring.open(fromLaptop, aad, 'phone@dev'), 'bound to its sender');
});

test('files encrypted on one kind of page decrypt on the other', async () => {
  const data = new Uint8Array(2.5 * 1024 * 1024).map((_, i) => (i * 31) & 255);
  const blob = new Blob([data]);
  const a = await fallback.encryptFile(blob);
  const back = new Uint8Array(await (await native.decryptFile(await a.blob.arrayBuffer(), a.file)).arrayBuffer());
  assert.deepEqual(back, data);
  const b = await native.encryptFile(blob);
  const again = new Uint8Array(await (await fallback.decryptFile(await b.blob.arrayBuffer(), b.file)).arrayBuffer());
  assert.deepEqual(again, data);
  const tampered = new Uint8Array(await b.blob.arrayBuffer());
  tampered[100] ^= 1;
  await assert.rejects(fallback.decryptFile(tampered, b.file));
});

test('security codes match', async () => {
  const devices = [await native.generateDeviceKeys(), await fallback.generateDeviceKeys()].map((k) => ({ publicKey: k.publicJwk }));
  assert.equal(await native.securityCode(devices), await fallback.securityCode(devices));
});

test('keys that are not on the curve are refused', async () => {
  const k = await native.generateDeviceKeys();
  const other = await native.generateDeviceKeys();
  const bad = { kty: 'EC', crv: 'P-256', x: k.publicJwk.x, y: other.publicJwk.y };
  await assert.rejects(fallback.importPublicKey(bad));
  await assert.rejects(shim.importKey('jwk', { ...k.publicJwk, d: 'secret' }, { name: 'ECDH', namedCurve: 'P-256' }, true, []), 'private keys are not public keys');
});

test('the stand-in matches WebCrypto byte for byte', async () => {
  const enc = new TextEncoder();
  const ikm = real.getRandomValues(new Uint8Array(32));
  const params = { name: 'HKDF', hash: 'SHA-256', salt: enc.encode('salt'), info: enc.encode('info') };
  const kw = async (lib) => lib.deriveKey(params, await lib.importKey('raw', ikm, 'HKDF', false, ['deriveKey']), { name: 'AES-KW', length: 256 }, false, ['wrapKey', 'unwrapKey']);
  const raw = real.getRandomValues(new Uint8Array(32));
  const content = async (lib) => lib.importKey('raw', raw, 'AES-GCM', true, ['encrypt']);
  const wrapped1 = new Uint8Array(await real.subtle.wrapKey('raw', await content(real.subtle), await kw(real.subtle), 'AES-KW'));
  const wrapped2 = new Uint8Array(await shim.wrapKey('raw', await content(shim), await kw(shim), 'AES-KW'));
  assert.deepEqual(wrapped2, wrapped1);
  const iv = real.getRandomValues(new Uint8Array(12));
  const alg = { name: 'AES-GCM', iv, additionalData: enc.encode('aad') };
  const ct1 = new Uint8Array(await real.subtle.encrypt(alg, await content(real.subtle), enc.encode('same bytes')));
  const ct2 = new Uint8Array(await shim.encrypt(alg, await content(shim), enc.encode('same bytes')));
  assert.deepEqual(ct2, ct1);
  await assert.rejects(shim.wrapKey('raw', await shim.importKey('raw', raw, 'AES-GCM', false, ['encrypt']), await kw(shim), 'AES-KW'), 'non-extractable keys stay put');
});
