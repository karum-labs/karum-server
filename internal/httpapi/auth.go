package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/karum-labs/karum-server/internal/auth"
)

type loginBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type registerBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// postLogin checks an email/password pair and returns an opaque session token.
func (h *Handler) postLogin(c *gin.Context) {
	if !h.loginLimiter.Allow(c.ClientIP()) {
		writeError(c, http.StatusTooManyRequests, "rate_limited", "too many sign-in attempts; retry shortly")
		return
	}
	if h.auth == nil || !h.auth.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "auth_not_configured", "accounts are not configured on this deployment")
		return
	}
	body, err := readLoginBody(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "validation", err.Error())
		return
	}
	session, err := h.auth.Login(c.Request.Context(), body.Email, body.Password)
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// postRegister creates an account from an email and password and signs it in.
func (h *Handler) postRegister(c *gin.Context) {
	if !h.loginLimiter.Allow(c.ClientIP()) {
		writeError(c, http.StatusTooManyRequests, "rate_limited", "too many sign-in attempts; retry shortly")
		return
	}
	if h.auth == nil || !h.auth.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "auth_not_configured", "accounts are not configured on this deployment")
		return
	}
	body, err := readRegisterBody(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "validation", err.Error())
		return
	}
	session, err := h.auth.Register(c.Request.Context(), body.Email, body.Password)
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusCreated, session)
}

// postJudgeLogin creates a session for the preseeded judge without a password.
// The route only exists while HACKATHON_MODE is on; the public demo never
// depends on it.
func (h *Handler) postJudgeLogin(c *gin.Context) {
	if h.cfg == nil || !h.cfg.HackathonMode {
		writeError(c, http.StatusNotFound, "judge_mode_disabled", "judge sign-in is disabled")
		return
	}
	if h.auth == nil || !h.auth.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "auth_not_configured", "the judge account is not configured")
		return
	}
	session, err := h.auth.JudgeLogin(c.Request.Context())
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// getMe returns the account attached by the required-auth middleware.
func (h *Handler) getMe(c *gin.Context) {
	account, ok := auth.Current(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "unauthorized", "sign in is required")
		return
	}
	c.JSON(http.StatusOK, gin.H{"account": account})
}

// postLogout revokes the presented session.
func (h *Handler) postLogout(c *gin.Context) {
	if h.auth == nil {
		writeError(c, http.StatusServiceUnavailable, "auth_not_configured", "accounts are not configured on this deployment")
		return
	}
	token := auth.BearerToken(c.GetHeader("Authorization"))
	if err := h.auth.Logout(c.Request.Context(), token); err != nil {
		writeAuthError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func readLoginBody(c *gin.Context) (loginBody, error) {
	var body loginBody
	if err := decodeAuthJSON(c, &body); err != nil {
		return body, err
	}
	if len(body.Email) > 320 || len(body.Password) > 256 {
		return body, errors.New("email or password is too long")
	}
	if body.Email == "" || body.Password == "" {
		return body, errors.New("email and password are required")
	}
	return body, nil
}

func readRegisterBody(c *gin.Context) (registerBody, error) {
	var body registerBody
	if err := decodeAuthJSON(c, &body); err != nil {
		return body, err
	}
	if len(body.Email) > 320 || len(body.Password) > 256 {
		return body, errors.New("email or password is too long")
	}
	if body.Email == "" || body.Password == "" {
		return body, errors.New("email and password are required")
	}
	return body, nil
}

func decodeAuthJSON(c *gin.Context, dest any) error {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<16))
	if err != nil {
		return errors.New("could not read the request body")
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return errors.New("request body is not valid JSON")
	}
	return nil
}

func writeAuthError(c *gin.Context, err error) {
	var validation *auth.ValidationError
	if errors.As(err, &validation) {
		writeError(c, http.StatusBadRequest, "invalid_request", validation.Message)
		return
	}
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(c, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
	case errors.Is(err, auth.ErrUnauthorized):
		writeError(c, http.StatusUnauthorized, "unauthorized", "sign in is required")
	case errors.Is(err, auth.ErrEmailTaken):
		writeError(c, http.StatusConflict, "email_taken", "an account with this email already exists")
	case errors.Is(err, auth.ErrNoJudge):
		writeError(c, http.StatusServiceUnavailable, "judge_not_configured", "the judge account is not configured")
	case errors.Is(err, auth.ErrNotConfigured):
		writeError(c, http.StatusServiceUnavailable, "auth_not_configured", "accounts are not configured on this deployment")
	default:
		writeError(c, http.StatusServiceUnavailable, "auth_unavailable", "sign-in is temporarily unavailable")
	}
}

// optionalAuth attaches an account when a token is present and lets an
// anonymous request through otherwise.
func (h *Handler) optionalAuth() gin.HandlerFunc {
	return auth.Optional(h.auth)
}

// requiredAuth is only used by /auth/me and /auth/logout.
func (h *Handler) requiredAuth() gin.HandlerFunc {
	return auth.Require(h.auth)
}
