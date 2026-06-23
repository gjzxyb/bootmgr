package services

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"gorm.io/gorm"
)

type BootService struct {
	db      *gorm.DB
	baseURL string
}

func NewBootService(db *gorm.DB, cfg config.Config) BootService {
	return BootService{db: db, baseURL: strings.TrimRight(cfg.BootBaseURL, "/")}
}

func (s BootService) WithDB(db *gorm.DB) BootService {
	s.db = db
	return s
}

type BootRequest struct {
	MAC          string
	Architecture string
	Firmware     string
	RemoteAddr   string
	Source       string
}

func (s BootService) RenderIPXEScript(req BootRequest) (string, models.BootEvent, error) {
	mac := normalizeMAC(req.MAC)
	var server models.Server
	serverErr := s.db.Where("lower(primary_mac) = ?", mac).First(&server).Error
	discovered := false
	if errors.Is(serverErr, gorm.ErrRecordNotFound) && mac != "" {
		created, err := s.createDiscoveredServer(req, mac)
		if err != nil {
			return "", models.BootEvent{}, err
		}
		server = created
		serverErr = nil
		discovered = true
	}

	var deployment models.Deployment
	var deploymentID *uint
	if serverErr == nil && !discovered {
		if err := s.db.Where("server_id = ? AND status IN ?", server.ID, []string{"pending", "running"}).Order("created_at desc").First(&deployment).Error; err == nil {
			deploymentID = &deployment.ID
		}
	}

	script := s.discoveryScript(req)
	var serverID *uint
	if serverErr == nil {
		serverID = &server.ID
		if !discovered {
			if deploymentID == nil {
				script = managedIdleScript(server)
			} else {
				var err error
				script, err = s.installScript(server, deployment)
				if err != nil {
					return "", models.BootEvent{}, err
				}
			}
		}
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "unknown"
	}
	event := models.BootEvent{MAC: mac, Architecture: req.Architecture, Firmware: req.Firmware, RemoteAddr: req.RemoteAddr, Source: source, ServerID: serverID, DeploymentID: deploymentID, Script: redactMetadataTokens(script)}
	if err := s.db.Create(&event).Error; err != nil {
		return "", event, err
	}
	return script, event, nil
}

func (s BootService) createDiscoveredServer(req BootRequest, mac string) (models.Server, error) {
	now := time.Now().UTC()
	arch := strings.TrimSpace(req.Architecture)
	if arch == "" {
		arch = "x86_64"
	}
	hostname := "discovered-" + strings.NewReplacer(":", "-", ".", "-", "_", "-").Replace(mac)
	server := models.Server{Hostname: hostname, Status: "discovered", Architecture: arch, PrimaryMAC: mac, DiscoveredAt: &now, Notes: "Auto-discovered from PXE boot event"}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("lower(primary_mac) = ?", mac).Attrs(server).FirstOrCreate(&server).Error; err != nil {
			return err
		}
		history := models.ServerStatusHistory{ServerID: server.ID, ToStatus: "discovered", Reason: "boot.discovery", ActorEmail: "system"}
		return tx.Where("server_id = ? AND reason = ?", server.ID, history.Reason).FirstOrCreate(&history).Error
	})
	return server, err
}

func (s BootService) EnsureMetadataToken(serverID uint, deploymentID *uint) (models.MetadataToken, error) {
	var token models.MetadataToken
	query := s.db.Where("server_id = ?", serverID)
	if deploymentID != nil {
		query = query.Where("deployment_id = ?", *deploymentID)
	} else {
		query = query.Where("deployment_id IS NULL")
	}
	expires := time.Now().UTC().Add(24 * time.Hour)
	if err := query.First(&token).Error; err == nil {
		if token.ExpiresAt == nil || time.Now().UTC().Before(*token.ExpiresAt) {
			return token, nil
		}
		token.Token = randomToken()
		token.ExpiresAt = &expires
		token.LastUsedAt = nil
		return token, s.db.Model(&token).Updates(map[string]any{"token": token.Token, "expires_at": token.ExpiresAt, "last_used_at": nil}).Error
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return token, err
	}
	token = models.MetadataToken{Token: randomToken(), ServerID: serverID, DeploymentID: deploymentID, ExpiresAt: &expires}
	return token, s.db.Create(&token).Error
}

func (s BootService) discoveryScript(req BootRequest) string {
	return fmt.Sprintf("#!ipxe\necho Baremetal discovery for %s\necho Firmware: %s Architecture: %s\nchain %s/boot/discovery.ipxe || shell\n", req.MAC, req.Firmware, req.Architecture, s.baseURL)
}

func managedIdleScript(server models.Server) string {
	return fmt.Sprintf("#!ipxe\necho Server %s is managed but has no active deployment\nshell\n", server.Hostname)
}

func (s BootService) installScript(server models.Server, deployment models.Deployment) (string, error) {
	token, err := s.EnsureMetadataToken(server.ID, &deployment.ID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("#!ipxe\necho Installing %s with deployment %d\nset image-url %s/images/%d/file\nset metadata-url %s/metadata/by-token/%s\nset metadata-token %s\necho Image URL: ${image-url}\necho Metadata URL: ${metadata-url}\nchain %s/boot/linux-installer.ipxe || shell\n", server.Hostname, deployment.ID, s.baseURL, deployment.ImageID, s.baseURL, token.Token, token.Token, s.baseURL), nil
}

func (s BootService) DiscoveryScript() string {
	return fmt.Sprintf("#!ipxe\necho Starting hardware discovery environment\nset platform-url %s\necho Platform URL: ${platform-url}\nshell\n", s.baseURL)
}

func (s BootService) LinuxInstallerScript() string {
	return "#!ipxe\necho Starting Linux installer\necho image-url: ${image-url}\necho metadata-url: ${metadata-url}\necho This MVP exposes image and metadata URLs for the installer environment.\nshell\n"
}

func normalizeMAC(mac string) string {
	value := strings.TrimSpace(mac)
	if value == "" {
		return ""
	}
	if parsed, err := net.ParseMAC(value); err == nil {
		return strings.ToLower(parsed.String())
	}
	return strings.ToLower(value)
}

func randomToken() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func redactMetadataTokens(script string) string {
	const marker = "/metadata/by-token/"
	var out strings.Builder
	remaining := script
	for strings.Contains(remaining, marker) {
		idx := strings.Index(remaining, marker)
		out.WriteString(remaining[:idx])
		out.WriteString(marker)
		out.WriteString("<redacted>")
		tokenStart := idx + len(marker)
		tokenEnd := tokenStart
		for tokenEnd < len(remaining) {
			ch := remaining[tokenEnd]
			if ch == '\n' || ch == '\r' || ch == ' ' || ch == '\t' {
				break
			}
			tokenEnd++
		}
		remaining = remaining[tokenEnd:]
	}
	out.WriteString(remaining)

	lines := strings.Split(out.String(), "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		indent := line[:len(line)-len(trimmed)]
		if strings.HasPrefix(trimmed, "set metadata-token ") {
			lines[i] = indent + "set metadata-token <redacted>"
		}
	}
	return strings.Join(lines, "\n")
}
