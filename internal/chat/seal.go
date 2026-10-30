package chat

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
)

// The message envelope of web/crypto.js (PROTOCOL.md §2-5) in Go, for the
// radio bridge, which reads and writes messages on behalf of radio
// contacts: an AES-256-GCM content key, wrapped with AES-KW for each
// recipient device under a key from ECDH (P-256) and HKDF-SHA-256.

type envelope struct {
	V    int               `json:"v"`
	From string            `json:"from"`
	IV   string            `json:"iv"`
	CT   string            `json:"ct"`
	Keys map[string]string `json:"keys"`
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64u(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// parseJWK reads a device's public key as the devices table stores it.
func parseJWK(s string) (*ecdh.PublicKey, error) {
	var k struct {
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}
	if err := json.Unmarshal([]byte(s), &k); err != nil {
		return nil, err
	}
	x, errX := unb64u(k.X)
	y, errY := unb64u(k.Y)
	if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 || (k.Crv != "" && k.Crv != "P-256") {
		return nil, errors.New("not a P-256 key")
	}
	return ecdh.P256().NewPublicKey(append(append([]byte{4}, x...), y...))
}

// publicJWK is the stored form of a public key.
func publicJWK(k *ecdh.PublicKey) string {
	raw := k.Bytes() // 0x04 || x || y
	return mustJSON(map[string]string{"kty": "EC", "crv": "P-256", "x": b64u(raw[1:33]), "y": b64u(raw[33:65])})
}

// wrapKeyFor derives the key that wraps content keys from sender to
// recipient (always named in that order, so both ends get the same key).
func wrapKeyFor(priv *ecdh.PrivateKey, peer *ecdh.PublicKey, sender, recipient string) ([]byte, error) {
	shared, err := priv.ECDH(peer)
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, shared, []byte("tailchat/v1/wrap"), sender+"|"+recipient, 32)
}

// sealEnvelope encrypts content for the given devices (id -> key) and the
// sending device itself.
func sealEnvelope(from string, priv *ecdh.PrivateKey, recipients map[string]*ecdh.PublicKey, aad string, content any) (string, error) {
	pt, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	contentKey := make([]byte, 32)
	iv := make([]byte, 12)
	_, _ = rand.Read(contentKey)
	_, _ = rand.Read(iv)
	block, err := aes.NewCipher(contentKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	env := envelope{V: 1, From: from, IV: b64u(iv), CT: b64u(gcm.Seal(nil, iv, pt, []byte(aad))), Keys: map[string]string{}}
	all := map[string]*ecdh.PublicKey{from: priv.PublicKey()}
	for id, k := range recipients {
		all[id] = k
	}
	for id, k := range all {
		kek, err := wrapKeyFor(priv, k, from, id)
		if err != nil {
			return "", err
		}
		wrapped, err := aesKeyWrap(kek, contentKey)
		if err != nil {
			return "", err
		}
		env.Keys[id] = b64u(wrapped)
	}
	return mustJSON(env), nil
}

// openEnvelope decrypts a message for device me. senderKey looks up the
// sending device's key and its owner, which must be senderID.
func openEnvelope(payload, me string, priv *ecdh.PrivateKey, aad, senderID string,
	senderKey func(deviceID string) (*ecdh.PublicKey, string, error)) (map[string]any, error) {
	var env envelope
	if err := json.Unmarshal([]byte(payload), &env); err != nil || env.V != 1 || env.From == "" {
		return nil, errors.New("not a message envelope")
	}
	pub, owner, err := senderKey(env.From)
	if err != nil {
		return nil, err
	}
	if owner != senderID {
		return nil, errors.New("the message was sealed by another account's device")
	}
	wrapped, ok := env.Keys[me]
	if !ok {
		return nil, errors.New("not encrypted for this device")
	}
	w, err1 := unb64u(wrapped)
	iv, err2 := unb64u(env.IV)
	ct, err3 := unb64u(env.CT)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, err
	}
	kek, err := wrapKeyFor(priv, pub, env.From, me)
	if err != nil {
		return nil, err
	}
	contentKey, err := aesKeyUnwrap(kek, w)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(contentKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(iv) != gcm.NonceSize() {
		return nil, errors.New("bad nonce")
	}
	pt, err := gcm.Open(nil, iv, ct, []byte(aad))
	if err != nil {
		return nil, err
	}
	var content map[string]any
	if err := json.Unmarshal(pt, &content); err != nil || content == nil {
		return nil, errors.New("the message isn't a JSON object")
	}
	return content, nil
}

// AES key wrap (RFC 3394), which WebCrypto calls AES-KW.

var kwIV = []byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}

func aesKeyWrap(kek, key []byte) ([]byte, error) {
	if len(key)%8 != 0 || len(key) < 16 {
		return nil, errors.New("key wrap: bad key length")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	n := len(key) / 8
	a := binary.BigEndian.Uint64(kwIV)
	r := append([]byte(nil), key...)
	var buf [16]byte
	for j := range 6 {
		for i := range n {
			binary.BigEndian.PutUint64(buf[:8], a)
			copy(buf[8:], r[i*8:])
			block.Encrypt(buf[:], buf[:])
			a = binary.BigEndian.Uint64(buf[:8]) ^ uint64(n*j+i+1)
			copy(r[i*8:], buf[8:])
		}
	}
	return append(binary.BigEndian.AppendUint64(nil, a), r...), nil
}

func aesKeyUnwrap(kek, wrapped []byte) ([]byte, error) {
	if len(wrapped)%8 != 0 || len(wrapped) < 24 {
		return nil, errors.New("key unwrap: bad length")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	n := len(wrapped)/8 - 1
	a := binary.BigEndian.Uint64(wrapped)
	r := append([]byte(nil), wrapped[8:]...)
	var buf [16]byte
	for j := 5; j >= 0; j-- {
		for i := n - 1; i >= 0; i-- {
			binary.BigEndian.PutUint64(buf[:8], a^uint64(n*j+i+1))
			copy(buf[8:], r[i*8:(i+1)*8])
			block.Decrypt(buf[:], buf[:])
			a = binary.BigEndian.Uint64(buf[:8])
			copy(r[i*8:], buf[8:])
		}
	}
	if subtle.ConstantTimeCompare(binary.BigEndian.AppendUint64(nil, a), kwIV) != 1 {
		return nil, errors.New("key unwrap: integrity check failed")
	}
	return r, nil
}
