package handlers

import (
	"fmt"
	"strings"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"
)

func (h Handler) sshKnownHostsStatus() (string, string) {
	policy := strings.ToLower(strings.TrimSpace(h.cfg.SSHHostKeyPolicy))
	if policy == "" {
		policy = "insecure_ignore"
	}
	if policy != "known_hosts" {
		if h.sshModesEnabled() {
			return "warning", fmt.Sprintf("SSH_HOST_KEY_POLICY=%s; configured SSH targets will not be pinned to known_hosts", policy)
		}
		return "ok", "SSH known_hosts coverage is not required while SSH modes are simulated"
	}
	if h.db == nil {
		return "error", "database handle is not initialized"
	}
	targets, err := h.sshKnownHostsTargets()
	if err != nil {
		return "error", err.Error()
	}
	coverage, err := config.CheckKnownHostsCoverage(h.cfg.SSHKnownHostsPath, targets)
	if err != nil {
		return "error", err.Error()
	}
	if coverage.Total == 0 {
		return "ok", "known_hosts file is readable; no SSH targets are configured"
	}
	if len(coverage.Missing) == 0 {
		if coverage.HashedEntries > 0 {
			return "ok", fmt.Sprintf("known_hosts covers %d SSH target(s), including hashed host pattern matching", coverage.Total)
		}
		return "ok", fmt.Sprintf("known_hosts covers %d SSH target(s)", coverage.Total)
	}
	missing := knownHostsMissingLabels(coverage.Missing)
	return "error", fmt.Sprintf("known_hosts does not contain entries for %d configured SSH target(s): %s", len(coverage.Missing), strings.Join(missing, ", "))
}

func (h Handler) sshKnownHostsTargets() ([]config.KnownHostsTarget, error) {
	var accesses []models.SSHAccess
	if err := h.db.Select("host", "port").Order("id asc").Find(&accesses).Error; err != nil {
		return nil, err
	}
	targets := make([]config.KnownHostsTarget, 0, len(accesses))
	for _, access := range accesses {
		host := strings.TrimSpace(access.Host)
		if host == "" {
			continue
		}
		port := access.Port
		if port <= 0 {
			port = 22
		}
		targets = append(targets, config.KnownHostsTarget{Host: host, Port: port})
	}
	return targets, nil
}

func (h Handler) sshModesEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(h.cfg.CollectorMode), "ssh") ||
		strings.EqualFold(strings.TrimSpace(h.cfg.SSHOperationsMode), "ssh")
}

func knownHostsMissingLabels(targets []config.KnownHostsTarget) []string {
	const maxLabels = 5
	labels := make([]string, 0, len(targets))
	for i, target := range targets {
		if i >= maxLabels {
			labels = append(labels, fmt.Sprintf("...%d more", len(targets)-maxLabels))
			break
		}
		labels = append(labels, config.KnownHostsTargetLabel(target))
	}
	return labels
}
