package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const accountKey = "karum.auth.account"

// Optional attaches an account when a valid bearer token is present and lets an
// anonymous request through otherwise. This is what keeps the public demo
// usable: a session is attribution, not a wall.
func Optional(service *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := BearerToken(c.GetHeader("Authorization"))
		if token == "" {
			c.Next()
			return
		}
		if !service.Enabled() {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "auth_not_configured",
				"message": "accounts are not configured on this deployment",
			})
			return
		}
		account, err := service.Authenticate(c.Request.Context(), token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "unauthorized",
				"message": "your session has expired; sign in again",
			})
			return
		}
		c.Set(accountKey, account)
		c.Next()
	}
}

// Require rejects an anonymous request. Only the account endpoints use it; the
// scout and campaign routes stay optional-auth so the demo has no login wall.
func Require(service *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !service.Enabled() {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "auth_not_configured",
				"message": "accounts are not configured on this deployment",
			})
			return
		}
		token := BearerToken(c.GetHeader("Authorization"))
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "unauthorized",
				"message": "sign in is required",
			})
			return
		}
		account, err := service.Authenticate(c.Request.Context(), token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "unauthorized",
				"message": "your session has expired; sign in again",
			})
			return
		}
		c.Set(accountKey, account)
		c.Next()
	}
}

// Current returns the account attached by Optional or Require.
func Current(c *gin.Context) (Account, bool) {
	value, ok := c.Get(accountKey)
	if !ok {
		return Account{}, false
	}
	account, ok := value.(Account)
	return account, ok
}

// BearerToken extracts the raw token from an Authorization header.
func BearerToken(header string) string {
	parts := strings.Fields(strings.TrimSpace(header))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}
