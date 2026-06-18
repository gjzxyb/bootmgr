package middleware

import (
	"net/http"
	"strings"

	"baremetal-platform/backend/internal/services"

	"github.com/gin-gonic/gin"
)

func Auth(auth services.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		claims, err := auth.Parse(strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		c.Set("user_id", claims.UserID)
		c.Set("user_email", claims.Email)
		c.Set("user_role", claims.Role)
		c.Next()
	}
}

func RequireRole(allowed ...string) gin.HandlerFunc {
	allowedSet := map[string]bool{}
	for _, role := range allowed {
		allowedSet[role] = true
	}
	return func(c *gin.Context) {
		role := c.GetString("user_role")
		if !allowedSet[role] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
			return
		}
		c.Next()
	}
}

func RequireConfirmation(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("X-Confirm-Action") != action {
			c.AbortWithStatusJSON(http.StatusPreconditionRequired, gin.H{"error": "confirmation required", "required_header": "X-Confirm-Action", "required_value": action})
			return
		}
		c.Next()
	}
}

func Actor(c *gin.Context) (uint, string) {
	userID, _ := c.Get("user_id")
	email, _ := c.Get("user_email")
	id, _ := userID.(uint)
	actor, _ := email.(string)
	return id, actor
}
