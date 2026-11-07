package pii

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

const (
	// labelPOSSigningKey: subkey HKDF terpisah dari subkey nomor HP, supaya
	// kebocoran satu kegunaan tidak membuka yang lain.
	labelPOSSigningKey = "mganik/pii/pos-signing-key/v1"
	aadPOSSigningKey   = "pos_devices.signing_key_enc"
)

// SigningKeyCipher mengenkripsi signing key HMAC device POS
// (pos_devices.signing_key_enc) dengan AES-256-GCM. Kunci diturunkan dari
// PII_ENC_KEY lewat HKDF-SHA256. Aman dipakai bersamaan.
//
// Inventaris nilai turunan signing key yang tersimpan: hanya ciphertext di
// pos_devices.signing_key_enc. Signing key polos hanya ada di respons
// provisioning (sekali, Cache-Control: no-store) dan di device.
type SigningKeyCipher struct {
	aead cipher.AEAD
}

// NewSigningKeyCipher menurunkan subkey dari kunci induk PII_ENC_KEY.
func NewSigningKeyCipher(master []byte) (*SigningKeyCipher, error) {
	if len(master) < MinMasterKeyBytes {
		return nil, fmt.Errorf("pii: kunci induk minimal %d byte", MinMasterKeyBytes)
	}
	key, err := hkdf.Key(sha256.New, master, nil, labelPOSSigningKey, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SigningKeyCipher{aead: aead}, nil
}

// signingKeyAAD mengikat ciphertext ke device (id 16 byte), supaya ciphertext
// tidak bisa dipindah ke baris device lain.
func signingKeyAAD(deviceID []byte) []byte {
	aad := make([]byte, 0, len(aadPOSSigningKey)+1+len(deviceID))
	aad = append(aad, aadPOSSigningKey...)
	aad = append(aad, 0)
	return append(aad, deviceID...)
}

// Seal mengenkripsi signing key dengan nonce acak (format sama dengan
// EncryptPhone: versi || nonce || ciphertext+tag).
func (c *SigningKeyCipher) Seal(signingKey, deviceID []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	out := make([]byte, 1+ns, 1+ns+len(signingKey)+c.aead.Overhead())
	out[0] = envelopeV1
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, fmt.Errorf("pii: nonce: %w", err)
	}
	return c.aead.Seal(out, out[1:], signingKey, signingKeyAAD(deviceID)), nil
}

// Open membuka hasil Seal untuk device yang sama. ErrDecrypt kalau
// ciphertext rusak, kunci induk berbeda, atau milik device lain.
func (c *SigningKeyCipher) Open(env, deviceID []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	if len(env) < 1+ns+c.aead.Overhead() || env[0] != envelopeV1 {
		return nil, ErrDecrypt
	}
	plain, err := c.aead.Open(nil, env[1:1+ns], env[1+ns:], signingKeyAAD(deviceID))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}
