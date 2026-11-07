package domain

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// Vektor dihasilkan klien referensi JS (Node 22) persis seperti yang
// didokumentasikan di OpenAPI:
//
//	const canonicalize = (v) => Array.isArray(v) ? '[' + v.map(canonicalize).join(',') + ']'
//	  : v !== null && typeof v === 'object'
//	    ? '{' + Object.keys(v).sort().map((k) => JSON.stringify(k) + ':' + canonicalize(v[k])).join(',') + '}'
//	    : JSON.stringify(v);
//	const obj = JSON.parse(raw); delete obj.signature;
//	createHmac('sha256', key).update(canonicalize(obj), 'utf8').digest('hex')
const (
	jsVectorRaw = `{"b":1,"signature":"zz","a":"x\u0001\"/é\n ","c":[3,{"z":null,"y":true}],` +
		`"€":0,"ä":-0,"😀":"emoji","￿":false}`
	jsVectorCanonicalHex = "7b2261223a22785c75303030315c222fc3a95c6ee280a8222c2262223a312c2263223a5b332c7b2279223a747275652c227a223a6e756c6c7d5d2c22c3a4223a302c22e282ac223a302c22f09f9880223a22656d6f6a69222c22efbfbf223a66616c73657d"
	jsVectorKeyHex       = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	jsVectorSignature    = "62d9ac8aa45ca99d91fc310b67e46875efda67661870f7d0464a4a5f3767f8dc"
	jsVectorPayloadHash  = "cc0c89ab577a35e695a2e5d7e41276099e75941bb7d799c23fb3384ba6d5953f"
)

func TestCanonicalPosJSON_MatchesJSReference(t *testing.T) {
	got, err := CanonicalPosJSON([]byte(jsVectorRaw))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString(jsVectorCanonicalHex)
	if string(got) != string(want) {
		t.Fatalf("kanonis berbeda dari JS:\n got %s\nwant %s", got, want)
	}
	key, _ := hex.DecodeString(jsVectorKeyHex)
	s := NewPosSigner(key)
	if sig := s.Sign(got); sig != jsVectorSignature {
		t.Fatalf("signature %s, ingin %s", sig, jsVectorSignature)
	}
	if h := PosPayloadHash(got); hex.EncodeToString(h[:]) != jsVectorPayloadHash {
		t.Fatalf("payload hash %x", h)
	}
	if !s.Verify(got, jsVectorSignature) || !s.Verify(got, strings.ToUpper(jsVectorSignature)) {
		t.Fatal("signature sah ditolak")
	}
	// Signer dipakai ulang (Reset per transaksi) tetap konsisten.
	if s.Sign(got) != jsVectorSignature {
		t.Fatal("signer tidak di-reset")
	}
}

func TestCanonicalPosJSON_OrderAndWhitespaceIndependent(t *testing.T) {
	a, err := CanonicalPosJSON([]byte(` { "z" : [ 1 , 2 ], "a": {"y":"é","x":null}, "signature":"apa saja" } `))
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalPosJSON([]byte(`{"a":{"x":null,"y":"é"},"z":[1,2]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || string(a) != `{"a":{"x":null,"y":"é"},"z":[1,2]}` {
		t.Fatalf("kanonis berbeda: %s vs %s", a, b)
	}
	// "signature" hanya dibuang di tingkat atas.
	c, _ := CanonicalPosJSON([]byte(`{"x":{"signature":1}}`))
	if string(c) != `{"x":{"signature":1}}` {
		t.Fatalf("signature bersarang ikut dibuang: %s", c)
	}
}

func TestCanonicalPosJSON_Rejects(t *testing.T) {
	cases := map[string]string{
		"bukan objek":         `[1,2]`,
		"key duplikat":        `{"a":1,"a":2}`,
		"pecahan":             `{"a":1.5}`,
		"eksponen":            `{"a":1e3}`,
		"di atas 2^53-1":      `{"a":9007199254740992}`,
		"data tambahan":       `{"a":1} {"b":2}`,
		"json rusak":          `{"a":}`,
		"utf8 tidak valid":    "{\"a\":\"\xff\"}",
		"terlalu dalam":       `{"a":` + strings.Repeat("[", 20) + strings.Repeat("]", 20) + `}`,
		"string tingkat atas": `"x"`,
	}
	for name, raw := range cases {
		if _, err := CanonicalPosJSON([]byte(raw)); !errors.Is(err, ErrCanonical) {
			t.Errorf("%s: ingin ErrCanonical, dapat %v", name, err)
		}
	}
	// Batas rentang aman tepat masih diterima.
	if got, err := CanonicalPosJSON([]byte(`{"a":9007199254740991,"b":-9007199254740991}`)); err != nil ||
		string(got) != `{"a":9007199254740991,"b":-9007199254740991}` {
		t.Fatalf("batas aman: %s %v", got, err)
	}
}

// Vektor surrogate dari Node 22 (canonicalize yang sama): surrogate tunggal
// ditulis \udxxx persis seperti JSON.stringify, pasangan escape jadi UTF-8,
// key diurutkan per unit UTF-16 (\udc00 sesudah "note").
const (
	jsLoneRaw          = `{"note":"a\ud83db","signature":"zz","\udc00k":"😀x\udfff","b":"/"}`
	jsLoneCanonicalHex = "7b2262223a222f222c226e6f7465223a22615c756438336462222c225c75646330306b223a22f09f9880785c7564666666227d"
	jsLoneSignature    = "c319caa0e2c10e2b9308d4794c08ece3b4a63a1b131a77a30a733d0b7f8c07fa"
)

func TestCanonicalPosJSON_LoneSurrogateMatchesJS(t *testing.T) {
	got, info, err := CanonicalPosJSONInfo([]byte(jsLoneRaw))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString(jsLoneCanonicalHex)
	if string(got) != string(want) {
		t.Fatalf("kanonis berbeda dari JS:\n got %s\nwant %s", got, want)
	}
	key, _ := hex.DecodeString(jsVectorKeyHex)
	if sig := NewPosSigner(key).Sign(got); sig != jsLoneSignature {
		t.Fatalf("signature %s, ingin %s", sig, jsLoneSignature)
	}
	if !info.LoneSurrogate {
		t.Fatal("surrogate tunggal harus dilaporkan")
	}
	// Pasangan surrogate yang di-escape tidak dianggap tunggal.
	if _, info, err := CanonicalPosJSONInfo([]byte(`{"a":"😀"}`)); err != nil || info.LoneSurrogate {
		t.Fatalf("pasangan surrogate: %v %+v", err, info)
	}
	// Dua surrogate tunggal berbeda bukan key duplikat.
	if _, _, err := CanonicalPosJSONInfo([]byte(`{"\ud800":1,"\ud801":2}`)); err != nil {
		t.Fatalf("key surrogate berbeda: %v", err)
	}
}

func TestCanonicalPosJSON_RejectsMalformedSyntax(t *testing.T) {
	for name, raw := range map[string]string{
		"koma akhir objek":   `{"a":1,}`,
		"koma akhir array":   `{"a":[1,]}`,
		"kontrol mentah":     "{\"a\":\"x\ty\"}",
		"escape asing":       `{"a":"\x"}`,
		"escape u pendek":    `{"a":"\u12"}`,
		"literal salah":      `{"a":tru}`,
		"tanpa titik dua":    `{"a" 1}`,
		"string tak ditutup": `{"a":"x}`,
		"nol di depan":       `{"a":01}`,
	} {
		if _, err := CanonicalPosJSON([]byte(raw)); !errors.Is(err, ErrCanonical) {
			t.Errorf("%s: ingin ErrCanonical, dapat %v", name, err)
		}
	}
	got, err := CanonicalPosJSON([]byte(" {\r\n\"b\" : [ ] , \"a\" : { } , \"c\":[true,false,null,-0,\"\\/\"] } \n"))
	if err != nil || string(got) != `{"a":{},"b":[],"c":[true,false,null,0,"/"]}` {
		t.Fatalf("kanonis %s %v", got, err)
	}
}

func TestPosSigner_VerifyRejectsMalformed(t *testing.T) {
	s := NewPosSigner([]byte("k"))
	for _, sig := range []string{"", "zz", strings.Repeat("g", 64), strings.Repeat("a", 63)} {
		if s.Verify([]byte("{}"), sig) {
			t.Fatalf("signature %q harus ditolak", sig)
		}
	}
}
