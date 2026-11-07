package controller

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/domain"
	"mganik/api/internal/dto"
	"mganik/api/internal/service"
)

const (
	// AnonCookieName: id acak anonim untuk fingerprint scan. Nilainya tidak
	// pernah disimpan mentah; DB hanya menyimpan HMAC-nya.
	AnonCookieName = "mg_vid"
	anonCookieTTL  = 400 * 24 * 60 * 60
	// TestIPGeoHeader: koordinat "IP" untuk uji/demo ("lat,lng"). Diabaikan
	// di production; prototype tidak memanggil layanan geolokasi IP.
	TestIPGeoHeader = "X-Test-IP-Geo"
)

var anonIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// PublicController menangani endpoint publik: verifikasi keaslian (tanpa
// login) serta klaim dan saldo poin (sesi konsumen).
type PublicController struct {
	verify     *service.VerifyService
	loyalty    *service.LoyaltyService
	log        *slog.Logger
	production bool
}

func NewPublicController(verify *service.VerifyService, loyalty *service.LoyaltyService, log *slog.Logger, production bool) *PublicController {
	return &PublicController{verify: verify, loyalty: loyalty, log: log, production: production}
}

// anonID membaca cookie anonim; kalau tidak ada atau formatnya salah, membuat
// id acak 128-bit baru dan memasang cookie HttpOnly.
func (h *PublicController) anonID(c *gin.Context) string {
	if v, err := c.Cookie(AnonCookieName); err == nil && anonIDPattern.MatchString(v) {
		return v
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	id := hex.EncodeToString(b[:])
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(AnonCookieName, id, anonCookieTTL, "/api/v1/public", "", h.production, true)
	return id
}

// testIPGeo membaca header koordinat uji di luar production.
func (h *PublicController) testIPGeo(c *gin.Context) *domain.GeoPoint {
	if h.production {
		return nil
	}
	raw := c.GetHeader(TestIPGeoHeader)
	if raw == "" {
		return nil
	}
	latS, lngS, ok := strings.Cut(raw, ",")
	if !ok {
		return nil
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(latS), 64)
	lng, err2 := strconv.ParseFloat(strings.TrimSpace(lngS), 64)
	p := domain.GeoPoint{Lat: lat, Lng: lng}
	if err1 != nil || err2 != nil || !p.Valid() {
		return nil
	}
	return &p
}

func publicGeoOf(lat, lng *float64) *domain.GeoPoint {
	if lat == nil || lng == nil {
		return nil
	}
	return &domain.GeoPoint{Lat: *lat, Lng: *lng}
}

// Verify: POST /api/v1/public/verify.
func (h *PublicController) Verify(c *gin.Context) {
	var req dto.VerifyRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.verify.Verify(c.Request.Context(), service.VerifyInput{
		RawCode: req.Code, GPS: publicGeoOf(req.Lat, req.Lng), AccuracyM: req.AccuracyM,
		ClientIP: c.ClientIP(), AnonID: h.anonID(c), IPGeoOverride: h.testIPGeo(c),
	})
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	out := dto.VerifyResponse{Result: res.Result, FirstVerifiedAt: res.FirstVerifiedAt, FirstVerifiedCity: res.FirstVerifiedCity}
	if res.ProductName != nil {
		out.Product = &dto.VerifyProduct{Name: res.ProductName, LotNo: res.LotNo, ExpDate: res.ExpDate}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// Claim: POST /api/v1/public/claims (sesi konsumen).
func (h *PublicController) Claim(c *gin.Context) {
	var req dto.ClaimRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.loyalty.Claim(c.Request.Context(), auth.FromContext(c.Request.Context()), service.ClaimInput{
		Code: req.Code, PIN: req.PIN, GPS: publicGeoOf(req.Lat, req.Lng), AccuracyM: req.AccuracyM,
		ClientIP: c.ClientIP(), AnonID: h.anonID(c),
	})
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(status, dto.ClaimResponse{
		ClaimID: res.Claim.ID.String(), Status: res.Claim.Status, Points: res.Claim.Points,
		AvailableAt: res.Claim.AvailableAt, Replayed: res.Replayed,
	})
}

// Balance: GET /api/v1/public/loyalty/balance (sesi konsumen).
func (h *PublicController) Balance(c *gin.Context) {
	b, err := h.loyalty.Balance(c.Request.Context(), auth.FromContext(c.Request.Context()))
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, dto.LoyaltyBalanceResponse{Available: b.Available, Pending: b.Pending})
}

// LoyaltyController menangani review klaim oleh staff.
type LoyaltyController struct {
	loyalty *service.LoyaltyService
	log     *slog.Logger
}

func NewLoyaltyController(loyalty *service.LoyaltyService, log *slog.Logger) *LoyaltyController {
	return &LoyaltyController{loyalty: loyalty, log: log}
}

// Review: POST /api/v1/loyalty/claims/{id}/review (loyalty.review).
func (h *LoyaltyController) Review(c *gin.Context) {
	id, ok := serialPathUUID(c, "id")
	if !ok {
		apperror.Write(c, h.log, service.ErrClaimNotFound)
		return
	}
	var req dto.ReviewClaimRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	claim, err := h.loyalty.Review(c.Request.Context(), auth.FromContext(c.Request.Context()), id, req.Decision, req.Note)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	reasons := []dto.ClaimRiskReason{}
	if len(claim.RiskReasons) > 0 {
		if err := json.Unmarshal(claim.RiskReasons, &reasons); err != nil {
			apperror.Write(c, h.log, err)
			return
		}
	}
	c.JSON(http.StatusOK, dto.ReviewClaimResponse{
		ClaimID: claim.ID.String(), Status: claim.Status, Points: claim.Points,
		RiskScore: claim.RiskScore, RiskReasons: reasons, ReviewRequired: claim.ReviewRequired,
		AvailableAt: claim.AvailableAt, DecidedAt: claim.DecidedAt,
	})
}
