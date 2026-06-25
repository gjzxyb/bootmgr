package services

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	var image models.Image
	if err := s.db.First(&image, deployment.ImageID).Error; err != nil {
		return "", err
	}
	spec := s.installerBootSpec(image, deployment, token)
	var builder strings.Builder
	builder.WriteString("#!ipxe\n")
	builder.WriteString(fmt.Sprintf("echo Installing %s with deployment %d\n", server.Hostname, deployment.ID))
	builder.WriteString(fmt.Sprintf("set base-url %s\n", s.baseURL))
	builder.WriteString(fmt.Sprintf("set image-url %s/images/%d/file\n", s.baseURL, deployment.ImageID))
	builder.WriteString(fmt.Sprintf("set metadata-url %s/metadata/by-token/%s\n", s.baseURL, token.Token))
	builder.WriteString(fmt.Sprintf("set metadata-token %s\n", token.Token))
	if spec.KernelURL != "" {
		builder.WriteString(fmt.Sprintf("set kernel-url %s\n", spec.KernelURL))
	}
	if spec.InitrdURL != "" {
		builder.WriteString(fmt.Sprintf("set initrd-url %s\n", spec.InitrdURL))
	}
	if spec.KernelParams != "" {
		builder.WriteString(fmt.Sprintf("set kernel-params %s\n", spec.KernelParams))
	}
	builder.WriteString("echo Image URL: ${image-url}\n")
	builder.WriteString("echo Metadata URL: ${metadata-url}\n")
	builder.WriteString(fmt.Sprintf("chain %s/boot/linux-installer.ipxe || shell\n", s.baseURL))
	return builder.String(), nil
}

func (s BootService) DiscoveryScript() string {
	return fmt.Sprintf("#!ipxe\necho Starting hardware discovery environment\nset platform-url %s\necho Platform URL: ${platform-url}\nshell\n", s.baseURL)
}

func (s BootService) LinuxInstallerScript() string {
	return "#!ipxe\n" +
		"echo Starting Linux installer\n" +
		"echo image-url: ${image-url}\n" +
		"echo metadata-url: ${metadata-url}\n" +
		"isset ${kernel-url} || goto missing_boot_config\n" +
		"isset ${initrd-url} || goto missing_boot_config\n" +
		"echo kernel-url: ${kernel-url}\n" +
		"echo initrd-url: ${initrd-url}\n" +
		"kernel ${kernel-url} ${kernel-params} || goto boot_failed\n" +
		"initrd ${initrd-url} || goto boot_failed\n" +
		"boot || goto boot_failed\n" +
		":missing_boot_config\n" +
		"echo Missing kernel-url or initrd-url.\n" +
		"echo Configure image tags or deployment variables: kernel_url, initrd_url, kernel_params.\n" +
		"echo Boot assets can be served from ${base-url}/boot-assets/...\n" +
		"shell\n" +
		":boot_failed\n" +
		"echo Linux installer boot failed.\n" +
		"shell\n"
}

type installerBootSpec struct {
	KernelURL    string
	InitrdURL    string
	KernelParams string
}

func (s BootService) installerBootSpec(image models.Image, deployment models.Deployment, token models.MetadataToken) installerBootSpec {
	imageValues := jsonObject(image.Tags)
	deploymentValues := jsonObject(deployment.Variables)
	context := map[string]string{
		"base_url":       s.baseURL,
		"image_url":      fmt.Sprintf("%s/images/%d/file", s.baseURL, deployment.ImageID),
		"metadata_url":   fmt.Sprintf("%s/metadata/by-token/%s", s.baseURL, token.Token),
		"metadata_token": token.Token,
	}
	spec := installerBootSpec{
		KernelURL:    s.bootURL(bootString(deploymentValues, imageValues, "kernel_url", "boot_kernel_url", "pxe_kernel_url")),
		InitrdURL:    s.bootURL(bootString(deploymentValues, imageValues, "initrd_url", "boot_initrd_url", "pxe_initrd_url")),
		KernelParams: expandBootTemplate(bootString(deploymentValues, imageValues, "kernel_params", "boot_kernel_params", "pxe_kernel_params", "cmdline"), context),
	}
	if spec.KernelParams == "" && spec.KernelURL != "" && spec.InitrdURL != "" {
		spec.KernelParams = defaultKernelParams(image)
	}
	return spec
}

func (s BootService) bootURL(raw string) string {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		return value
	}
	if strings.HasPrefix(value, "/") {
		return s.baseURL + value
	}
	return s.baseURL + "/boot-assets/" + strings.TrimLeft(value, "/")
}

func jsonObject(raw []byte) map[string]any {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}
	return object
}

func bootString(primary map[string]any, fallback map[string]any, keys ...string) string {
	for _, values := range []map[string]any{primary, fallback} {
		for _, key := range keys {
			if value, ok := values[key]; ok {
				switch typed := value.(type) {
				case string:
					if trimmed := strings.TrimSpace(typed); trimmed != "" {
						return trimmed
					}
				case fmt.Stringer:
					if trimmed := strings.TrimSpace(typed.String()); trimmed != "" {
						return trimmed
					}
				}
			}
		}
	}
	return ""
}

func expandBootTemplate(value string, context map[string]string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for key, replacement := range context {
		for _, pattern := range []string{"{{" + key + "}}", "{" + key + "}"} {
			value = strings.ReplaceAll(value, pattern, replacement)
		}
	}
	return value
}

func defaultKernelParams(image models.Image) string {
	osFamily := strings.ToLower(strings.TrimSpace(image.OSFamily))
	if strings.Contains(osFamily, "ubuntu") {
		return "ip=dhcp boot=casper netboot=url url=${image-url} autoinstall ds=nocloud-net;s=${metadata-url}/"
	}
	return "ip=dhcp"
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
