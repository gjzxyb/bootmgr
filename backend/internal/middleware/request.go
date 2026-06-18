package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const requestIDKey = "request_id"

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := normalizeRequestID(c.GetHeader("X-Request-ID"))
		if requestID == "" {
			requestID = newRequestID()
		}
		c.Set(requestIDKey, requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

func RequestIDValue(c *gin.Context) string {
	return c.GetString(requestIDKey)
}

func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now().UTC()
		c.Next()
		entry := map[string]any{
			"time":        start.Format(time.RFC3339Nano),
			"level":       "info",
			"request_id":  RequestIDValue(c),
			"method":      c.Request.Method,
			"path":        c.FullPath(),
			"raw_path":    c.Request.URL.Path,
			"status":      c.Writer.Status(),
			"latency_ms":  float64(time.Since(start).Microseconds()) / 1000,
			"client_ip":   c.ClientIP(),
			"user_agent":  c.Request.UserAgent(),
			"actor_email": c.GetString("user_email"),
		}
		if len(c.Errors) > 0 {
			entry["level"] = "error"
			entry["error"] = c.Errors.String()
		}
		_ = json.NewEncoder(gin.DefaultWriter).Encode(entry)
	}
}

func normalizeRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '/' {
			continue
		}
		return ""
	}
	return value
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000000")))
	}
	return hex.EncodeToString(b[:])
}
