package services

import (
	"encoding/json"
	"fmt"
	"strings"

	"baremetal-platform/backend/internal/models"
)

func RenderInstallTemplate(content string, server models.Server, deployment models.Deployment, metadataToken string) string {
	vars := map[string]string{
		"server_id":      fmt.Sprintf("%d", server.ID),
		"hostname":       server.Hostname,
		"asset_no":       server.AssetNo,
		"primary_ip":     server.PrimaryIP,
		"primary_mac":    server.PrimaryMAC,
		"architecture":   server.Architecture,
		"metadata_token": metadataToken,
	}
	var extra map[string]any
	if len(deployment.Variables) > 0 {
		_ = json.Unmarshal(deployment.Variables, &extra)
	}
	for key, value := range extra {
		vars[key] = fmt.Sprint(value)
	}

	rendered := content
	for key, value := range vars {
		rendered = strings.ReplaceAll(rendered, "{{"+key+"}}", value)
	}
	return rendered
}
