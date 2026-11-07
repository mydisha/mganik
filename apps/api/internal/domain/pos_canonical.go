package domain

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"regexp"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Kanonisasi transaksi POS untuk signature HMAC dan payload_hash (PLANNING
// §6.1 #6). Bentuknya subset RFC 8785 (JCS) yang bisa dihasilkan klien JS
// dengan "urutkan key objek secara rekursif lalu JSON.stringify":
//
//  1. Input adalah objek transaksi apa adanya dari klien, dikurangi member
//     tingkat atas "signature". Member lain (termasuk yang tidak dikenal
//     server) ikut ditandatangani.
//  2. Key objek diurutkan menurut unit kode UTF-16 (sama dengan sort()
//     bawaan JS; untuk key ASCII = urutan byte). Key duplikat ditolak.
//  3. Tanpa spasi di luar string. Urutan array dipertahankan.
//  4. String di-escape seperti JSON.stringify: \" \\ \b \f \n \r \t, karakter
//     kontrol lain U+0000-U+001F sebagai \u00xx (hex huruf kecil), karakter
//     lain (termasuk "/", non-ASCII, U+2028/U+2029) ditulis apa adanya dalam
//     UTF-8. Surrogate UTF-16 tunggal (misal "\ud83d" tanpa pasangan) ditulis
//     \udxxx huruf kecil, sama dengan JSON.stringify ES2019, supaya signature
//     tetap bisa diverifikasi; CanonicalInfo.LoneSurrogate melaporkannya dan
//     transaksi seperti itu ditolak sebagai payload tidak valid.
//  5. Angka hanya bilangan bulat dalam rentang aman JS (|n| <= 2^53-1),
//     ditulis desimal tanpa tanda plus, nol di depan, pecahan, atau eksponen
//     (-0 ditulis 0).
//  6. true, false, null apa adanya.
//
// signature = hex huruf kecil HMAC-SHA256(signing_key, kanonis);
// payload_hash = SHA-256(kanonis).

// PosSignatureField adalah member tingkat atas yang tidak ikut dikanonisasi.
const PosSignatureField = "signature"

// MaxSafeInteger adalah Number.MAX_SAFE_INTEGER JS (2^53 - 1).
const MaxSafeInteger = 1<<53 - 1

// maxCanonicalDepth membatasi kedalaman objek/array (anti stack abuse).
const maxCanonicalDepth = 16

var (
	// ErrCanonical: payload tidak bisa dikanonisasi (bukan objek JSON, key
	// duplikat, angka bukan bilangan bulat aman, terlalu dalam, ...).
	ErrCanonical = errors.New("payload tidak bisa dikanonisasi")

	canonicalIntPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
)

// CanonicalInfo melaporkan sifat payload yang terlihat saat kanonisasi.
type CanonicalInfo struct {
	// LoneSurrogate: ada string (nilai atau key) berisi surrogate UTF-16
	// tanpa pasangan. Bentuk kanonisnya tetap persis JSON.stringify, tetapi
	// isinya bukan teks Unicode valid.
	LoneSurrogate bool
}

// CanonicalPosJSON mengembalikan bentuk kanonis objek JSON raw tanpa member
// tingkat atas PosSignatureField.
func CanonicalPosJSON(raw []byte) ([]byte, error) {
	out, _, err := CanonicalPosJSONInfo(raw)
	return out, err
}

// CanonicalPosJSONInfo sama dengan CanonicalPosJSON plus CanonicalInfo.
// Parser sendiri (bukan encoding/json) dipakai supaya escape \uXXXX dibaca
// per unit UTF-16: encoding/json mengganti surrogate tunggal dengan U+FFFD
// sehingga bentuk kanonisnya berbeda dari yang ditandatangani klien JS.
func CanonicalPosJSONInfo(raw []byte) ([]byte, CanonicalInfo, error) {
	if !utf8.Valid(raw) {
		return nil, CanonicalInfo{}, fmt.Errorf("%w: bukan UTF-8 valid", ErrCanonical)
	}
	p := &canonParser{s: raw}
	p.ws()
	if p.peek() != '{' {
		return nil, CanonicalInfo{}, p.errf("transaksi harus objek JSON")
	}
	var buf bytes.Buffer
	if err := p.object(&buf, 1, true); err != nil {
		return nil, CanonicalInfo{}, err
	}
	p.ws()
	if p.i != len(p.s) {
		return nil, CanonicalInfo{}, p.errf("data tambahan setelah objek")
	}
	return buf.Bytes(), p.info, nil
}

// canonParser: parser JSON minimal (RFC 8259) yang langsung menulis bentuk
// kanonis. String disimpan sebagai unit UTF-16 untuk urutan key dan surrogate.
type canonParser struct {
	s    []byte
	i    int
	info CanonicalInfo
}

// signatureKey: PosSignatureField dalam encoding key internal (unitsKey).
var signatureKey = unitsKey(utf16.Encode([]rune(PosSignatureField)))

// unitsKey: unit UTF-16 sebagai string byte (big-endian) untuk peta key;
// dua surrogate tunggal berbeda tidak pernah dianggap sama.
func unitsKey(units []uint16) string {
	b := make([]byte, 2*len(units))
	for i, u := range units {
		b[2*i], b[2*i+1] = byte(u>>8), byte(u)
	}
	return string(b)
}

func (p *canonParser) errf(format string, a ...any) error {
	return fmt.Errorf("%w: %s (posisi %d)", ErrCanonical, fmt.Sprintf(format, a...), p.i)
}

func (p *canonParser) ws() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *canonParser) peek() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func (p *canonParser) value(buf *bytes.Buffer, depth int) error {
	p.ws()
	switch c := p.peek(); {
	case c == '{' || c == '[':
		if depth >= maxCanonicalDepth {
			return p.errf("terlalu dalam")
		}
		if c == '{' {
			return p.object(buf, depth+1, false)
		}
		return p.array(buf, depth+1)
	case c == '"':
		units, err := p.str()
		if err != nil {
			return err
		}
		p.writeUnits(buf, units)
		return nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number(buf)
	}
	for _, lit := range []string{"true", "false", "null"} {
		if bytes.HasPrefix(p.s[p.i:], []byte(lit)) {
			p.i += len(lit)
			buf.WriteString(lit)
			return nil
		}
	}
	return p.errf("nilai JSON tidak valid")
}

func (p *canonParser) object(buf *bytes.Buffer, depth int, top bool) error {
	type member struct {
		units []uint16
		value []byte
	}
	var members []member
	seen := map[string]bool{}
	p.i++ // '{'
	p.ws()
	if p.peek() == '}' {
		p.i++
	} else {
		for {
			p.ws()
			if p.peek() != '"' {
				return p.errf("key objek harus string")
			}
			units, err := p.str()
			if err != nil {
				return err
			}
			k := unitsKey(units)
			if seen[k] {
				return p.errf("key duplikat")
			}
			seen[k] = true
			p.ws()
			if p.peek() != ':' {
				return p.errf("':' diharapkan")
			}
			p.i++
			var vb bytes.Buffer
			if err := p.value(&vb, depth); err != nil {
				return err
			}
			if !top || k != signatureKey {
				members = append(members, member{units: units, value: vb.Bytes()})
			}
			p.ws()
			if p.peek() == ',' {
				p.i++
				continue
			}
			if p.peek() != '}' {
				return p.errf("',' atau '}' diharapkan")
			}
			p.i++
			break
		}
	}
	slices.SortFunc(members, func(a, b member) int { return slices.Compare(a.units, b.units) })
	buf.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			buf.WriteByte(',')
		}
		p.writeUnits(buf, m.units)
		buf.WriteByte(':')
		buf.Write(m.value)
	}
	buf.WriteByte('}')
	return nil
}

func (p *canonParser) array(buf *bytes.Buffer, depth int) error {
	p.i++ // '['
	buf.WriteByte('[')
	p.ws()
	if p.peek() == ']' {
		p.i++
		buf.WriteByte(']')
		return nil
	}
	for i := 0; ; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := p.value(buf, depth); err != nil {
			return err
		}
		p.ws()
		switch p.peek() {
		case ',':
			p.i++
		case ']':
			p.i++
			buf.WriteByte(']')
			return nil
		default:
			return p.errf("',' atau ']' diharapkan")
		}
	}
}

// number hanya menerima bilangan bulat aman (aturan 5).
func (p *canonParser) number(buf *bytes.Buffer) error {
	start := p.i
	for p.i < len(p.s) && bytes.IndexByte([]byte("0123456789.eE+-"), p.s[p.i]) >= 0 {
		p.i++
	}
	s := string(p.s[start:p.i])
	if !canonicalIntPattern.MatchString(s) {
		return p.errf("angka %q bukan bilangan bulat", s)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n > MaxSafeInteger || n < -MaxSafeInteger {
		return p.errf("angka %q di luar rentang aman", s)
	}
	buf.WriteString(strconv.FormatInt(n, 10))
	return nil
}

// str membaca string JSON menjadi unit UTF-16 (escape \uXXXX apa adanya).
func (p *canonParser) str() ([]uint16, error) {
	p.i++ // '"'
	var units []uint16
	for {
		if p.i >= len(p.s) {
			return nil, p.errf("string tidak ditutup")
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return units, nil
		case c < 0x20:
			return nil, p.errf("karakter kontrol mentah di dalam string")
		case c == '\\':
			if p.i+1 >= len(p.s) {
				return nil, p.errf("escape tidak lengkap")
			}
			e := p.s[p.i+1]
			p.i += 2
			switch e {
			case '"', '\\', '/':
				units = append(units, uint16(e))
			case 'b':
				units = append(units, '\b')
			case 'f':
				units = append(units, '\f')
			case 'n':
				units = append(units, '\n')
			case 'r':
				units = append(units, '\r')
			case 't':
				units = append(units, '\t')
			case 'u':
				if p.i+4 > len(p.s) {
					return nil, p.errf("escape \\u tidak lengkap")
				}
				v, err := strconv.ParseUint(string(p.s[p.i:p.i+4]), 16, 16)
				if err != nil {
					return nil, p.errf("escape \\u tidak valid")
				}
				units = append(units, uint16(v))
				p.i += 4
			default:
				return nil, p.errf("escape tidak dikenal")
			}
		case c < utf8.RuneSelf:
			units = append(units, uint16(c))
			p.i++
		default:
			r, size := utf8.DecodeRune(p.s[p.i:]) // raw sudah dicek UTF-8 valid
			units = utf16.AppendRune(units, r)
			p.i += size
		}
	}
}

// writeUnits menulis string dengan escape JSON.stringify (ES2019): pasangan
// surrogate jadi karakter UTF-8, surrogate tunggal jadi \udxxx.
func (p *canonParser) writeUnits(buf *bytes.Buffer, units []uint16) {
	const hexDigits = "0123456789abcdef"
	buf.WriteByte('"')
	for i := 0; i < len(units); i++ {
		r := rune(units[i])
		if utf16.IsSurrogate(r) {
			if r < 0xdc00 && i+1 < len(units) && units[i+1] >= 0xdc00 && units[i+1] <= 0xdfff {
				r = utf16.DecodeRune(r, rune(units[i+1]))
				i++
			} else {
				p.info.LoneSurrogate = true
				u := units[i]
				buf.WriteString(`\u`)
				buf.WriteByte(hexDigits[u>>12])
				buf.WriteByte(hexDigits[(u>>8)&0xf])
				buf.WriteByte(hexDigits[(u>>4)&0xf])
				buf.WriteByte(hexDigits[u&0xf])
				continue
			}
		}
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				buf.WriteString(`\u00`)
				buf.WriteByte(hexDigits[r>>4])
				buf.WriteByte(hexDigits[r&0xf])
				continue
			}
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}

// PosPayloadHash adalah SHA-256 bentuk kanonis (pos_transactions.payload_hash).
func PosPayloadHash(canonical []byte) [32]byte { return sha256.Sum256(canonical) }

// PosSigner menghitung dan memeriksa signature transaksi satu device. Dibuat
// sekali per batch (satu hmac.New), lalu di-Reset per transaksi.
type PosSigner struct {
	mac hash.Hash
}

// NewPosSigner memakai signing key device (32 byte).
func NewPosSigner(key []byte) *PosSigner { return &PosSigner{mac: hmac.New(sha256.New, key)} }

// Sign mengembalikan hex huruf kecil HMAC-SHA256(key, canonical).
func (s *PosSigner) Sign(canonical []byte) string {
	s.mac.Reset()
	_, _ = s.mac.Write(canonical)
	return hex.EncodeToString(s.mac.Sum(nil))
}

// Verify membandingkan signature hex (64 karakter) dalam waktu konstan.
func (s *PosSigner) Verify(canonical []byte, signatureHex string) bool {
	if len(signatureHex) != sha256.Size*2 {
		return false
	}
	got, err := hex.DecodeString(signatureHex)
	if err != nil {
		return false
	}
	s.mac.Reset()
	_, _ = s.mac.Write(canonical)
	return hmac.Equal(s.mac.Sum(nil), got)
}
