package controller

import (
	"encoding"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"

	"mganik/api/internal/apperror"
)

var setupValidatorOnce sync.Once

// SetupValidator membuat pesan validasi memakai nama field JSON, bukan nama
// struct Go. Dipanggil sekali oleh router.
func SetupValidator() {
	setupValidatorOnce.Do(func() {
		v, ok := binding.Validator.Engine().(*validator.Validate)
		if !ok {
			return
		}
		v.RegisterTagNameFunc(func(f reflect.StructField) string {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				return ""
			}
			if name == "" {
				return f.Name
			}
			return name
		})
	})
}

// BindJSON membaca body JSON ke dst dan menjalankan validasi tag `binding`.
// Field yang tidak dikenal ditolak (mencegah mass assignment). Error yang
// dikembalikan sudah berupa *apperror.Error: 413 body_too_large,
// 400 invalid_request, atau 422 validation_failed dengan daftar field.
func BindJSON(c *gin.Context, dst any) error {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return apperror.ErrBadRequest
	}
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return apperror.ErrBodyTooLarge
		}
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field != "" {
			return apperror.Validation(apperror.FieldError{
				Field:   dottedToIndexed(typeErr.Field),
				Code:    "invalid_type",
				Message: "harus berupa " + jsonTypeName(typeErr.Type),
			})
		}
		return apperror.ErrBadRequest
	}
	if dec.More() {
		return apperror.ErrBadRequest
	}
	if err := binding.Validator.ValidateStruct(dst); err != nil {
		var verrs validator.ValidationErrors
		if !errors.As(err, &verrs) {
			return apperror.ErrBadRequest
		}
		fields := make([]apperror.FieldError, 0, len(verrs))
		for _, fe := range verrs {
			fields = append(fields, apperror.FieldError{
				Field:   jsonPath(fe.Namespace()),
				Code:    fe.Tag(),
				Message: validationMessage(fe),
			})
		}
		return apperror.Validation(fields...)
	}
	return nil
}

// dottedToIndexed menyamakan path encoding/json ("lines.0.qty") dengan format
// validator ("lines[0].qty").
func dottedToIndexed(path string) string {
	parts := strings.Split(path, ".")
	var b strings.Builder
	for i, p := range parts {
		if _, err := strconv.Atoi(p); err == nil && i > 0 {
			b.WriteString("[" + p + "]")
			continue
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(p)
	}
	return b.String()
}

// jsonPath membuang nama struct akar dari namespace validator
// ("ReserveRequest.lines[0].qty" → "lines[0].qty").
func jsonPath(ns string) string {
	if _, rest, ok := strings.Cut(ns, "."); ok {
		return rest
	}
	return ns
}

// validationMessage menulis pesan field dalam bahasa Indonesia. Batas untuk
// teks dihitung dalam karakter, untuk daftar dalam item, selain itu nilai.
func validationMessage(fe validator.FieldError) string {
	param := formatParam(fe.Param()) + lengthUnit(fe.Kind())
	switch fe.Tag() {
	case "required":
		// Untuk angka, nilai nol dianggap kosong oleh validator.
		if isNumericKind(fe.Kind()) {
			return "wajib diisi dan tidak boleh 0"
		}
		return "wajib diisi"
	case "required_with":
		return "wajib diisi bersama " + goFieldsToJSON(fe.Param())
	case "ne":
		return "tidak boleh " + param
	case "min", "gte":
		return "minimal " + param
	case "max", "lte":
		return "maksimal " + param
	case "gt":
		return "harus lebih dari " + param
	case "oneof":
		return "harus salah satu dari: " + strings.ReplaceAll(fe.Param(), " ", ", ")
	case "uuid", "uuid4", "uuid7":
		return "harus UUID yang valid"
	case "len":
		return "harus tepat " + param
	case "unique":
		return "tidak boleh berisi nilai ganda"
	case "dive":
		return "berisi nilai yang tidak valid"
	default:
		return "tidak valid (aturan " + fe.Tag() + ")"
	}
}

// formatParam menulis parameter angka dengan pengelompokan id-ID
// ("1000000" → "1.000.000"); parameter lain dikembalikan apa adanya.
func formatParam(p string) string {
	if n, err := strconv.ParseInt(p, 10, 64); err == nil {
		return apperror.FormatNumber(n)
	}
	return p
}

func isNumericKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

// goFieldsToJSON mengubah nama field Go di parameter validator (mis.
// required_with=Lng ProgramCode) menjadi nama JSON snake_case ("lng,
// program_code"), supaya pesan tidak memuat nama struct Go.
func goFieldsToJSON(param string) string {
	names := strings.Fields(param)
	for i, n := range names {
		var b strings.Builder
		for j, r := range n {
			if j > 0 && r >= 'A' && r <= 'Z' {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
		}
		names[i] = b.String()
	}
	return strings.Join(names, ", ")
}

// lengthUnit: satuan batas min/max/len menurut jenis field.
func lengthUnit(k reflect.Kind) string {
	switch k {
	case reflect.String:
		return " karakter"
	case reflect.Slice, reflect.Array, reflect.Map:
		return " item"
	default:
		return ""
	}
}

var textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()

// jsonTypeName menamai tipe tujuan decode JSON dengan istilah pengguna,
// bukan nama tipe Go (int64, []dto.Line).
func jsonTypeName(t reflect.Type) string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return "nilai yang valid"
	}
	// uuid.UUID, time.Time, dsb. dikirim sebagai string JSON.
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return "teks"
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "bilangan bulat"
	case reflect.Float32, reflect.Float64:
		return "angka"
	case reflect.String:
		return "teks"
	case reflect.Bool:
		return "true atau false"
	case reflect.Slice, reflect.Array:
		return "daftar"
	case reflect.Map, reflect.Struct:
		return "objek"
	default:
		return "nilai yang valid"
	}
}
