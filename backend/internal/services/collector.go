package services

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"gorm.io/gorm"
)

type Collector interface {
	Collect(ctx context.Context, target models.SSHAccess) ([]models.MetricSample, error)
}

type CollectorService struct {
	db        *gorm.DB
	collector Collector
}

const MetricRetention = 7 * 24 * time.Hour

func NewCollectorService(db *gorm.DB, cfg config.Config) CollectorService {
	collector := Collector(SimulatedCollector{})
	if strings.ToLower(strings.TrimSpace(cfg.CollectorMode)) == "ssh" {
		collector = SSHCommandCollector{executor: NewSSHCommandExecutor(db, cfg)}
	}
	return CollectorService{db: db, collector: collector}
}

func (s CollectorService) StartAgentlessCollection(serverID uint, requestedBy string) (models.CollectionJob, error) {
	now := time.Now().UTC()
	job := models.CollectionJob{ServerID: serverID, Mode: "ssh_agentless", Status: "running", RequestedBy: requestedBy, StartedAt: &now}
	if err := s.db.Create(&job).Error; err != nil {
		return job, err
	}
	go s.run(job.ID)
	return job, nil
}

func (s CollectorService) run(jobID uint) {
	var job models.CollectionJob
	if err := s.db.First(&job, jobID).Error; err != nil {
		return
	}
	var target models.SSHAccess
	_ = s.db.Where("server_id = ?", job.ServerID).First(&target).Error
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	samples, err := s.collector.Collect(ctx, target)
	if err != nil {
		s.db.Model(&job).Updates(map[string]any{"status": "failed", "error_message": err.Error(), "finished_at": time.Now().UTC()})
		return
	}
	for i := range samples {
		samples[i].ServerID = job.ServerID
	}
	s.db.Create(&samples)
	_ = s.pruneOldMetrics(time.Now().UTC())
	s.db.Model(&job).Updates(map[string]any{"status": "success", "finished_at": time.Now().UTC()})
}

func (s CollectorService) pruneOldMetrics(now time.Time) error {
	cutoff := now.Add(-MetricRetention)
	return s.db.Where("collected_at < ?", cutoff).Delete(&models.MetricSample{}).Error
}

type SimulatedCollector struct{}

func (SimulatedCollector) Collect(ctx context.Context, target models.SSHAccess) ([]models.MetricSample, error) {
	time.Sleep(300 * time.Millisecond)
	now := time.Now().UTC()
	return []models.MetricSample{{MetricName: "host_up", Value: 1, Unit: "bool", CollectedAt: now}, {MetricName: "cpu_usage", Value: 35, Unit: "%", CollectedAt: now}, {MetricName: "memory_usage", Value: 58, Unit: "%", CollectedAt: now}, {MetricName: "disk_usage", Value: 47, Unit: "%", CollectedAt: now}, {MetricName: "disk_smart_health", Value: 0, Unit: "bool", CollectedAt: now}, {MetricName: "network_rx_mbps", Value: 128, Unit: "Mbps", CollectedAt: now}, {MetricName: "network_tx_mbps", Value: 64, Unit: "Mbps", CollectedAt: now}, {MetricName: "process_count", Value: 142, Unit: "count", CollectedAt: now}, {MetricName: "process_zombie_count", Value: 0, Unit: "count", CollectedAt: now}}, nil
}

type SSHCommandCollector struct {
	executor SSHCommandExecutor
}

func (c SSHCommandCollector) Collect(ctx context.Context, target models.SSHAccess) ([]models.MetricSample, error) {
	if target.Host == "" {
		return nil, errors.New("ssh target is not configured")
	}
	cmdText := "echo host_up=1; printf 'cpu_usage=%s\\n' $(awk 'NR==1 {print 100-($5*100/($2+$3+$4+$5+$6+$7+$8))}' /proc/stat); free | awk '/Mem:/ {printf \"memory_usage=%.0f\\n\", $3*100/$2}'; df -P / | awk 'NR==2 {gsub(/%/,\"\",$5); print \"disk_usage=\"$5}'; if command -v smartctl >/dev/null 2>&1; then if smartctl -H /dev/sda 2>/dev/null | grep -Eiq 'FAILED|FAIL|BAD'; then echo disk_smart_health=1; else echo disk_smart_health=0; fi; else echo disk_smart_health=0; fi; awk 'NR>2 {rx+=$2; tx+=$10} END {printf \"network_rx_mbps=%.2f\\nnetwork_tx_mbps=%.2f\\n\", rx*8/1000000, tx*8/1000000}' /proc/net/dev; process_count=$(ps -e -o pid= 2>/dev/null | wc -l | tr -d ' '); echo process_count=${process_count:-0}; zombie_count=$(ps -e -o stat= 2>/dev/null | awk '$1 ~ /Z/ {z++} END {print z+0}'); echo process_zombie_count=${zombie_count:-0}"
	result, err := c.executor.Run(ctx, target, cmdText)
	if err != nil {
		return nil, fmt.Errorf("ssh collection failed: %w: %s", err, strings.TrimSpace(result.Stderr))
	}
	return parseMetricLines(result.Stdout, time.Now().UTC()), nil
}

func parseMetricLines(text string, collectedAt time.Time) []models.MetricSample {
	var samples []models.MetricSample
	for _, line := range strings.Split(text, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 {
			continue
		}
		value, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			continue
		}
		metricName := parts[0]
		samples = append(samples, models.MetricSample{MetricName: metricName, Value: value, Unit: metricUnit(metricName), CollectedAt: collectedAt})
	}
	return samples
}

func metricUnit(metricName string) string {
	switch metricName {
	case "host_up", "disk_smart_health":
		return "bool"
	case "network_rx_mbps", "network_tx_mbps":
		return "Mbps"
	case "process_count", "process_zombie_count":
		return "count"
	default:
		return "%"
	}
}
