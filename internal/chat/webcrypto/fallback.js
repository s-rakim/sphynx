// The parts of WebCrypto (crypto.subtle) that Sphynx Chat uses, for pages
// opened over plain http, where browsers don't offer crypto.subtle. Chats
// opened at http://sphynx-home/ stay end-to-end encrypted with the same
// algorithms as on HTTPS (ECDH P-256, HKDF-SHA-256, AES-KW, AES-256-GCM),
// built on the audited noble libraries. One difference: keys are plain
// objects here, so the page itself could read its private key, where
// WebCrypto would keep it out of reach of script.
//
// After changing this file, rebuild web/vendor/webcrypto-fallback.js:
//   cd internal/chat/webcrypto && npm ci && npm run build
import { aeskw, gcm } from '@noble/ciphers/aes.js';
import { p256 } from '@noble/curves/nist.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';

function opError(message) {
  const err = new Error(message);
  err.name = 'OperationError';
  return err;
}

function bytes(data) {
  if (data instanceof Uint8Array) return data;
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  if (ArrayBuffer.isView(data)) return new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
  throw new TypeError('Expected an ArrayBuffer or a typed array');
}

// A copy in its own ArrayBuffer, like WebCrypto returns.
const out = (u8) => u8.slice().buffer;

const algName = (alg) => String(typeof alg === 'string' ? alg : alg?.name).toUpperCase();

function b64u(u8) {
  let s = '';
  for (const b of u8) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function unb64u(text) {
  const s = atob(String(text).replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((String(text).length + 3) % 4));
  const u8 = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) u8[i] = s.charCodeAt(i);
  return u8;
}

// Keys are plain objects, so they can be kept in IndexedDB like CryptoKeys.
function key(type, algorithm, extractable, usages, raw) {
  return { type, algorithm, extractable: !!extractable, usages: [...(usages || [])], raw };
}

function need(k, name, usage) {
  if (!k || !(k.raw instanceof Uint8Array) || algName(k.algorithm) !== name) throw opError(`Expected a ${name} key`);
  if (usage && !k.usages.includes(usage)) throw opError(`The key can't be used to ${usage}`);
  return k.raw;
}

function aesLength(raw) {
  if (![16, 24, 32].includes(raw.length)) throw opError('AES keys are 128, 192 or 256 bits');
  return raw;
}

const ECDH = { name: 'ECDH', namedCurve: 'P-256' };

export const subtle = {
  async generateKey(algorithm, extractable, usages) {
    const name = algName(algorithm);
    if (name === 'ECDH') {
      if (algorithm.namedCurve !== 'P-256') throw opError('Only P-256 is supported');
      const secret = p256.utils.randomSecretKey();
      const pub = p256.getPublicKey(secret, false);
      return {
        privateKey: key('private', ECDH, extractable, usages.filter((u) => u === 'deriveBits' || u === 'deriveKey'), secret),
        publicKey: key('public', ECDH, true, [], pub),
      };
    }
    if (name === 'AES-GCM' || name === 'AES-KW') {
      const raw = crypto.getRandomValues(new Uint8Array((algorithm.length || 256) / 8));
      return key('secret', { name, length: raw.length * 8 }, extractable, usages, aesLength(raw));
    }
    throw opError(`${name} is not supported`);
  },

  async exportKey(format, k) {
    if (!k.extractable) throw opError('The key is not extractable');
    if (format === 'raw') return out(k.raw);
    if (format === 'jwk' && k.type === 'public' && algName(k.algorithm) === 'ECDH') {
      return { kty: 'EC', crv: 'P-256', x: b64u(k.raw.subarray(1, 33)), y: b64u(k.raw.subarray(33, 65)), ext: true, key_ops: [] };
    }
    throw opError(`Can't export this key as ${format}`);
  },

  async importKey(format, data, algorithm, extractable, usages) {
    const name = algName(algorithm);
    if (name === 'ECDH') {
      let pub;
      if (format === 'jwk') {
        if (data?.kty !== 'EC' || data?.crv !== 'P-256' || data.d) throw opError('Expected a P-256 public key');
        const x = unb64u(data.x);
        const y = unb64u(data.y);
        if (x.length !== 32 || y.length !== 32) throw opError('Malformed P-256 key');
        pub = new Uint8Array(65);
        pub[0] = 4;
        pub.set(x, 1);
        pub.set(y, 33);
      } else if (format === 'raw') pub = bytes(data).slice();
      else throw opError(`Can't import an ECDH key as ${format}`);
      // Rejects points that are not on the curve (invalid-curve attacks).
      if (!p256.utils.isValidPublicKey(pub, false)) throw opError('Invalid P-256 public key');
      return key('public', ECDH, extractable, [], pub);
    }
    if (format !== 'raw') throw opError(`Can't import a ${name} key as ${format}`);
    if (name === 'HKDF') return key('secret', { name }, false, usages, bytes(data).slice());
    if (name === 'AES-GCM' || name === 'AES-KW') {
      const raw = aesLength(bytes(data).slice());
      return key('secret', { name, length: raw.length * 8 }, extractable, usages, raw);
    }
    throw opError(`${name} is not supported`);
  },

  async digest(algorithm, data) {
    if (algName(algorithm) !== 'SHA-256') throw opError('Only SHA-256 is supported');
    return out(sha256(bytes(data)));
  },

  async deriveBits(algorithm, baseKey, length) {
    if (algName(algorithm) !== 'ECDH') throw opError('Only ECDH is supported');
    if (baseKey.type !== 'private') throw opError('Expected a private key');
    const secret = need(baseKey, 'ECDH', 'deriveBits');
    const pub = need(algorithm.public, 'ECDH');
    // WebCrypto's ECDH output is the shared point's x coordinate.
    const x = p256.getSharedSecret(secret, pub, true).subarray(1);
    if (length !== undefined && length !== null && length !== 256) throw opError('P-256 gives 256 bits');
    return out(x);
  },

  async deriveKey(algorithm, baseKey, derivedAlgorithm, extractable, usages) {
    if (algName(algorithm) !== 'HKDF' || algName(algorithm.hash) !== 'SHA-256') throw opError('Only HKDF with SHA-256 is supported');
    const name = algName(derivedAlgorithm);
    if (name !== 'AES-KW' && name !== 'AES-GCM') throw opError(`Can't derive a ${name} key`);
    const ikm = need(baseKey, 'HKDF', 'deriveKey');
    const raw = hkdf(sha256, ikm, bytes(algorithm.salt), bytes(algorithm.info), (derivedAlgorithm.length || 256) / 8);
    return key('secret', { name, length: raw.length * 8 }, extractable, usages, aesLength(raw));
  },

  async encrypt(algorithm, k, data) {
    if (algName(algorithm) !== 'AES-GCM') throw opError('Only AES-GCM is supported');
    const raw = need(k, 'AES-GCM', 'encrypt');
    const aad = algorithm.additionalData === undefined ? undefined : bytes(algorithm.additionalData);
    return out(gcm(raw, bytes(algorithm.iv), aad).encrypt(bytes(data)));
  },

  async decrypt(algorithm, k, data) {
    if (algName(algorithm) !== 'AES-GCM') throw opError('Only AES-GCM is supported');
    const raw = need(k, 'AES-GCM', 'decrypt');
    const aad = algorithm.additionalData === undefined ? undefined : bytes(algorithm.additionalData);
    try {
      return out(gcm(raw, bytes(algorithm.iv), aad).decrypt(bytes(data)));
    } catch {
      throw opError('The data could not be decrypted');
    }
  },

  async wrapKey(format, k, wrappingKey, wrapAlgorithm) {
    if (format !== 'raw' || algName(wrapAlgorithm) !== 'AES-KW') throw opError('Only raw keys wrapped with AES-KW are supported');
    if (!k.extractable) throw opError('The key is not extractable');
    return out(aeskw(need(wrappingKey, 'AES-KW', 'wrapKey')).encrypt(k.raw));
  },

  async unwrapKey(format, wrapped, unwrappingKey, unwrapAlgorithm, keyAlgorithm, extractable, usages) {
    if (format !== 'raw' || algName(unwrapAlgorithm) !== 'AES-KW') throw opError('Only raw keys wrapped with AES-KW are supported');
    let raw;
    try {
      raw = aeskw(need(unwrappingKey, 'AES-KW', 'unwrapKey')).decrypt(bytes(wrapped));
    } catch (err) {
      if (err.name === 'OperationError') throw err;
      throw opError('The key could not be unwrapped');
    }
    return subtle.importKey('raw', raw, keyAlgorithm, extractable, usages);
  },
};
