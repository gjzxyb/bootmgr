package services

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"time"

	"baremetal-platform/backend/internal/models"

	"gorm.io/gorm"
)

type BOMService struct{ db *gorm.DB }

func NewBOMService(db *gorm.DB) BOMService { return BOMService{db: db} }

type BOMRow struct {
	ServerID       uint       `json:"server_id"`
	AssetNo        string     `json:"asset_no"`
	Hostname       string     `json:"hostname"`
	Status         string     `json:"status"`
	SerialNumber   string     `json:"serial_number"`
	Architecture   string     `json:"architecture"`
	PrimaryIP      string     `json:"primary_ip"`
	PrimaryMAC     string     `json:"primary_mac"`
	Owner          string     `json:"owner"`
	Location       string     `json:"location"`
	Rack           string     `json:"rack"`
	RackUnit       string     `json:"rack_unit"`
	CPUSummary     string     `json:"cpu_summary"`
	MemorySummary  string     `json:"memory_summary"`
	DiskSummary    string     `json:"disk_summary"`
	NetworkSummary string     `json:"network_summary"`
	GPUSummary     string     `json:"gpu_summary"`
	RAIDSummary    string     `json:"raid_summary"`
	CollectedBy    string     `json:"collected_by"`
	CollectedAt    *time.Time `json:"collected_at"`
}

func (s BOMService) ForServer(serverID string) (BOMRow, error) {
	var server models.Server
	if err := s.db.First(&server, serverID).Error; err != nil {
		return BOMRow{}, err
	}
	var inventory models.HardwareInventory
	err := s.db.Where("server_id = ?", server.ID).Order("collected_at desc, id desc").First(&inventory).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return BOMRow{}, err
	}
	return bomFrom(server, inventory, err == nil), nil
}

func (s BOMService) All() ([]BOMRow, error) {
	var servers []models.Server
	if err := s.db.Order("asset_no asc, hostname asc").Find(&servers).Error; err != nil {
		return nil, err
	}
	rows := make([]BOMRow, 0, len(servers))
	for _, server := range servers {
		var inventory models.HardwareInventory
		err := s.db.Where("server_id = ?", server.ID).Order("collected_at desc, id desc").First(&inventory).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return nil, err
		}
		rows = append(rows, bomFrom(server, inventory, err == nil))
	}
	return rows, nil
}

func BOMCSV(rows []BOMRow) ([]byte, error) {
	buf := bytes.Buffer{}
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{"asset_no", "hostname", "status", "serial_number", "architecture", "primary_ip", "primary_mac", "owner", "location", "rack", "rack_unit", "cpu_summary", "memory_summary", "disk_summary", "network_summary", "gpu_summary", "raid_summary", "collected_by", "collected_at", "server_id"}); err != nil {
		return nil, err
	}
	for _, row := range rows {
		collectedAt := ""
		if row.CollectedAt != nil {
			collectedAt = row.CollectedAt.Format(time.RFC3339)
		}
		if err := w.Write([]string{row.AssetNo, row.Hostname, row.Status, row.SerialNumber, row.Architecture, row.PrimaryIP, row.PrimaryMAC, row.Owner, row.Location, row.Rack, row.RackUnit, row.CPUSummary, row.MemorySummary, row.DiskSummary, row.NetworkSummary, row.GPUSummary, row.RAIDSummary, row.CollectedBy, collectedAt, strconv.FormatUint(uint64(row.ServerID), 10)}); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func bomFrom(server models.Server, inventory models.HardwareInventory, hasInventory bool) BOMRow {
	row := BOMRow{ServerID: server.ID, AssetNo: server.AssetNo, Hostname: server.Hostname, Status: server.Status, SerialNumber: server.SerialNumber, Architecture: server.Architecture, PrimaryIP: server.PrimaryIP, PrimaryMAC: server.PrimaryMAC, Owner: server.Owner, Location: server.Location, Rack: server.Rack, RackUnit: server.RackUnit}
	if hasInventory {
		row.CPUSummary = inventory.CPUSummary
		row.MemorySummary = inventory.MemorySummary
		row.DiskSummary = inventory.DiskSummary
		row.NetworkSummary = inventory.NetworkSummary
		row.GPUSummary = inventory.GPUSummary
		row.RAIDSummary = inventory.RAIDSummary
		row.CollectedBy = inventory.CollectedBy
		row.CollectedAt = &inventory.CollectedAt
	}
	return row
}
