// Package pii melindungi data pribadi yang harus disimpan: nomor HP konsumen
// dienkripsi AES-256-GCM, dicari lewat blind index HMAC-SHA256, dan IP hanya
// disimpan sebagai prefix HMAC. Semua subkey diturunkan dari satu kunci induk
// (PII_ENC_KEY) lewat HKDF-SHA256 dengan label berbeda, sehingga kunci
// enkripsi tidak pernah sama dengan kunci blind index.
//
// Inventaris nilai turunan nomor HP yang tersimpan: consumers.phone_enc
// (ciphertext), consumers.phone_bidx dan otp_challenges.phone_bidx (HMAC,
// deterministik). Perlindungan efektif = yang terlemah, yaitu blind index:
// siapa pun yang memegang kunci induk bisa menebak nomor lewat HMAC. Tanpa
// kunci, dump DB tidak membuka nomor.
package pii

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// MinMasterKeyBytes: kunci induk lebih pendek ditolak di semua lingkungan
// (config.Load memvalidasi batas yang sama saat start).
const MinMasterKeyBytes = 32

const (
	// envelopeV1 = byte versi || nonce 12 byte || ciphertext+tag.
	envelopeV1 byte = 1
	// IPHashLen: hash IP dipotong 16 byte (cukup untuk korelasi, tidak
	// menyimpan HMAC penuh).
	IPHashLen = 16

	labelPhoneEnc  = "mganik/pii/phone-enc/v1"
	labelPhoneBidx = "mganik/pii/phone-bidx/v1"
	labelIPHash    = "mganik/pii/ip-hash/v1"
	labelOTP       = "mganik/pii/otp-code/v1"
	aadPhone       = "consumers.phone_enc"
)

var ErrDecrypt = errors.New("pii: ciphertext tidak valid atau kunci berbeda")

// Keys memegang subkey turunan. Aman dipakai bersamaan.
type Keys struct {
	phoneAEAD cipher.AEAD
	bidxKey   []byte
	ipKey     []byte
	otpKey    []byte
}

// New menurunkan semua subkey dari kunci induk.
func New(master []byte) (*Keys, error) {
	if len(master) < MinMasterKeyBytes {
		return nil, fmt.Errorf("pii: kunci induk minimal %d byte", MinMasterKeyBytes)
	}
	derive := func(label string) ([]byte, error) {
		return hkdf.Key(sha256.New, master, nil, label, 32)
	}
	encKey, err := derive(labelPhoneEnc)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	k := &Keys{phoneAEAD: aead}
	if k.bidxKey, err = derive(labelPhoneBidx); err != nil {
		return nil, err
	}
	if k.ipKey, err = derive(labelIPHash); err != nil {
		return nil, err
	}
	if k.otpKey, err = derive(labelOTP); err != nil {
		return nil, err
	}
	return k, nil
}

// phoneAAD mengikat ciphertext ke barisnya (blind index), supaya ciphertext
// tidak bisa dipindah/ditukar ke baris konsumen lain.
func phoneAAD(rowBinding []byte) []byte {
	aad := make([]byte, 0, len(aadPhone)+1+len(rowBinding))
	aad = append(aad, aadPhone...)
	aad = append(aad, 0)
	return append(aad, rowBinding...)
}

// EncryptPhone mengenkripsi nomor (E.164) dengan nonce acak, jadi nomor yang
// sama menghasilkan ciphertext berbeda setiap kali. rowBinding (blind index
// baris, consumers.phone_bidx) masuk AAD dan wajib sama saat dekripsi.
func (k *Keys) EncryptPhone(e164 string, rowBinding []byte) ([]byte, error) {
	ns := k.phoneAEAD.NonceSize()
	out := make([]byte, 1+ns, 1+ns+len(e164)+k.phoneAEAD.Overhead())
	out[0] = envelopeV1
	nonce := out[1:]
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("pii: nonce: %w", err)
	}
	return k.phoneAEAD.Seal(out, nonce, []byte(e164), phoneAAD(rowBinding)), nil
}

// DecryptPhone membuka hasil EncryptPhone dengan rowBinding yang sama.
func (k *Keys) DecryptPhone(env, rowBinding []byte) (string, error) {
	ns := k.phoneAEAD.NonceSize()
	if len(env) < 1+ns+k.phoneAEAD.Overhead() || env[0] != envelopeV1 {
		return "", ErrDecrypt
	}
	plain, err := k.phoneAEAD.Open(nil, env[1:1+ns], env[1+ns:], phoneAAD(rowBinding))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}

// PhoneIndex adalah blind index deterministik (HMAC-SHA256 32 byte) untuk
// mencari konsumen berdasarkan nomor tanpa menyimpan nomor polos.
func (k *Keys) PhoneIndex(e164 string) []byte {
	return k.mac(k.bidxKey, e164)
}

// HashIP mengembalikan prefix HMAC alamat IP. String kosong = nil.
func (k *Keys) HashIP(ip string) []byte {
	if ip == "" {
		return nil
	}
	return k.mac(k.ipKey, ip)[:IPHashLen]
}

// OTPCodeHash mengikat kode OTP ke satu challenge.
func (k *Keys) OTPCodeHash(challengeID []byte, code string) []byte {
	m := hmac.New(sha256.New, k.otpKey)
	m.Write(challengeID)
	m.Write([]byte{0})
	m.Write([]byte(code))
	return m.Sum(nil)
}

func (k *Keys) mac(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}
