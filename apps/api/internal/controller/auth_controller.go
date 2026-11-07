package controller

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/dto"
	"mganik/api/internal/model"
	"mganik/api/internal/service"
)

type authService interface {
	Login(ctx context.Context, in service.LoginInput) (service.LoginResult, error)
	Logout(ctx context.Context, token string, meta service.RequestMeta) error
	Me(ctx context.Context, userID uuid.UUID) (service.Me, error)
}

// AuthController: login, logout, dan profil staff.
type AuthController struct {
	svc authService
	log *slog.Logger
}

func NewAuthController(svc authService, log *slog.Logger) *AuthController {
	return &AuthController{svc: svc, log: log}
}

// Login: POST /auth/login. Sukses → cookie sesi + profil (seperti /auth/me).
func (h *AuthController) Login(c *gin.Context) {
	var req dto.LoginRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.svc.Login(c.Request.Context(), service.LoginInput{
		Email:         req.Email,
		Password:      req.Password,
		PreviousToken: cookieValue(c, service.StaffSessionCookie),
		Meta:          requestMeta(c),
	})
	if err != nil {
		writeAuthError(c, h.log, err)
		return
	}
	// Profil disiapkan SEBELUM cookie dipasang: kalau gagal, respons error
	// tidak membawa cookie sesi hidup. Sesi di DB dicabut supaya tidak yatim.
	me, err := h.svc.Me(c.Request.Context(), res.UserID)
	if err != nil {
		if lerr := h.svc.Logout(c.Request.Context(), res.Token, requestMeta(c)); lerr != nil {
			h.log.ErrorContext(c.Request.Context(), "mencabut sesi setelah profil gagal", "error", lerr)
		}
		apperror.Write(c, h.log, err)
		return
	}
	body := toMeResponse(me)
	setSessionCookie(c, service.StaffSessionCookie, res.Token, service.StaffAbsoluteTimeout)
	c.JSON(http.StatusOK, body)
}

// Logout: POST /auth/logout. Selalu 204 dan menghapus cookie (idempotent).
func (h *AuthController) Logout(c *gin.Context) {
	if err := h.svc.Logout(c.Request.Context(), cookieValue(c, service.StaffSessionCookie), requestMeta(c)); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	clearSessionCookie(c, service.StaffSessionCookie)
	c.Status(http.StatusNoContent)
}

// Me: GET /auth/me (butuh principal staff).
func (h *AuthController) Me(c *gin.Context) {
	p := auth.FromContext(c.Request.Context())
	if p == nil || p.Kind != auth.KindStaff {
		apperror.Write(c, h.log, apperror.ErrUnauthenticated)
		return
	}
	me, err := h.svc.Me(c.Request.Context(), p.ID)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, toMeResponse(me))
}

func toMeResponse(me service.Me) dto.MeResponse {
	out := dto.MeResponse{
		User:        dto.MeUser{ID: me.ID, Email: me.Email, Name: me.Name},
		Roles:       make([]dto.MeRole, 0, len(me.Roles)),
		Permissions: me.Permissions,
		Scope:       dto.MeScope{Mode: model.ScopeSelected, Locations: toLocationRefs(me.Locations)},
	}
	if me.ScopeAll {
		out.Scope.Mode = model.ScopeAll
	}
	for _, r := range me.Roles {
		out.Roles = append(out.Roles, dto.MeRole{Code: r.Code, Name: r.Name, ScopeMode: r.ScopeMode, Locations: toLocationRefs(r.Locations)})
	}
	return out
}

func toLocationRefs(in []service.Location) []dto.LocationRef {
	out := make([]dto.LocationRef, 0, len(in))
	for _, l := range in {
		out = append(out, dto.LocationRef{ID: l.ID, Code: l.Code, Name: l.Name})
	}
	return out
}

// ------------------------------------------------------------------ helper cookie & error

func requestMeta(c *gin.Context) service.RequestMeta {
	return service.RequestMeta{IP: c.ClientIP(), UserAgent: c.Request.UserAgent(), RequestID: c.GetString(apperror.RequestIDKey)}
}

func cookieValue(c *gin.Context, name string) string {
	ck, err := c.Request.Cookie(name)
	if err != nil {
		return ""
	}
	return ck.Value
}

// setSessionCookie memasang cookie __Host-: Secure, HttpOnly, SameSite=Lax,
// Path=/, tanpa Domain.
func setSessionCookie(c *gin.Context, name, token string, maxAge time.Duration) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		MaxAge:   int(maxAge / time.Second),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(c *gin.Context, name string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// writeAuthError menambahkan Retry-After untuk 429 lalu menulis problem+json.
func writeAuthError(c *gin.Context, log *slog.Logger, err error) {
	var ra *service.RetryAfterError
	if errors.As(err, &ra) && ra.After > 0 {
		c.Header("Retry-After", strconv.Itoa(int(ra.After/time.Second)))
	}
	apperror.Write(c, log, err)
}
