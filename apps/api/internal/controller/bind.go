package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"

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
				Message: fmt.Sprintf("harus bertipe %s", typeErr.Type),
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

func validationMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "wajib diisi"
	case "min", "gte":
		return "minimal " + fe.Param()
	case "max", "lte":
		return "maksimal " + fe.Param()
	case "gt":
		return "harus lebih besar dari " + fe.Param()
	case "oneof":
		return "harus salah satu dari: " + fe.Param()
	case "uuid", "uuid4", "uuid7":
		return "harus UUID yang valid"
	case "len":
		return "panjang harus " + fe.Param()
	case "dive", "unique":
		return "berisi nilai yang tidak valid atau duplikat"
	default:
		return "tidak valid (" + fe.Tag() + ")"
	}
}
