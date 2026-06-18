package services

import (
	"strconv"

	"baremetal-platform/backend/internal/models"

	"gorm.io/gorm"
)

type AuditService struct{ db *gorm.DB }

func NewAuditService(db *gorm.DB) AuditService { return AuditService{db: db} }

func (s AuditService) Record(actorID uint, actorEmail, action, resourceType string, resourceID any, risk, clientIP, userAgent string, requestID ...string) {
	if risk == "" {
		risk = "low"
	}
	rid := ""
	if len(requestID) > 0 {
		rid = requestID[0]
	}
	log := models.AuditLog{ActorID: actorID, ActorEmail: actorEmail, Action: action, ResourceType: resourceType, ResourceID: idString(resourceID), RiskLevel: risk, RequestID: rid, ClientIP: clientIP, UserAgent: userAgent}
	_ = s.db.Create(&log).Error
}

func idString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case uint:
		return strconv.FormatUint(uint64(x), 10)
	case int:
		return strconv.Itoa(x)
	default:
		return ""
	}
}
