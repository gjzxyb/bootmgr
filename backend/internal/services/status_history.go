package services

import (
	"baremetal-platform/backend/internal/models"

	"gorm.io/gorm"
)

type StatusHistoryService struct{ db *gorm.DB }

func NewStatusHistoryService(db *gorm.DB) StatusHistoryService { return StatusHistoryService{db: db} }

func (s StatusHistoryService) WithDB(db *gorm.DB) StatusHistoryService {
	s.db = db
	return s
}

func (s StatusHistoryService) Record(serverID uint, fromStatus, toStatus, reason, actorEmail string) error {
	if toStatus == "" || fromStatus == toStatus {
		return nil
	}
	row := models.ServerStatusHistory{ServerID: serverID, FromStatus: fromStatus, ToStatus: toStatus, Reason: reason, ActorEmail: actorEmail}
	return s.db.Create(&row).Error
}
