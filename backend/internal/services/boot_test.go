package services

import (
	"strings"
	"testing"
	"time"

	"baremetal-platform/backend/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func TestEnsureMetadataTokenRotatesExpiredToken(t *testing.T) {
	db := metadataTokenTestDB(t)
	expiredAt := time.Now().UTC().Add(-time.Hour)
	deploymentID := uint(7)
	expired := models.MetadataToken{Token: "expired-token", ServerID: 1, DeploymentID: &deploymentID, ExpiresAt: &expiredAt}
	if err := db.Create(&expired).Error; err != nil {
		t.Fatal(err)
	}

	token, err := (BootService{db: db}).EnsureMetadataToken(1, &deploymentID)
	if err != nil {
		t.Fatalf("ensure metadata token: %v", err)
	}
	if token.Token == "expired-token" {
		t.Fatalf("expected expired token to be rotated")
	}
	if token.ExpiresAt == nil || !token.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("expected rotated token to have a future expiry: %#v", token)
	}

	var count int64
	if err := db.Model(&models.MetadataToken{}).Where("server_id = ? AND deployment_id = ?", 1, deploymentID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected token rotation to update existing row, got %d rows", count)
	}
}

func TestEnsureMetadataTokenReusesValidToken(t *testing.T) {
	db := metadataTokenTestDB(t)
	expiresAt := time.Now().UTC().Add(time.Hour)
	token := models.MetadataToken{Token: "valid-token", ServerID: 1, ExpiresAt: &expiresAt}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}

	reused, err := (BootService{db: db}).EnsureMetadataToken(1, nil)
	if err != nil {
		t.Fatalf("ensure metadata token: %v", err)
	}
	if reused.Token != "valid-token" {
		t.Fatalf("expected valid token to be reused, got %q", reused.Token)
	}
}

func TestInstallerBootSpecUsesBootTagsAndExpandsPlaceholders(t *testing.T) {
	service := BootService{baseURL: "http://boot.test"}
	image := models.Image{
		ID:       9,
		OSFamily: "ubuntu",
		Tags: datatypes.JSON([]byte(`{
			"kernel_url": "ubuntu-24.04/casper/vmlinuz",
			"initrd_url": "/custom/initrd",
			"kernel_params": "ip=dhcp url={{image_url}} ds=nocloud-net;s={{metadata_url}}/"
		}`)),
	}
	deployment := models.Deployment{ImageID: 9}
	token := models.MetadataToken{Token: "metadata-token"}

	spec := service.installerBootSpec(image, deployment, token)
	if spec.KernelURL != "http://boot.test/boot-assets/ubuntu-24.04/casper/vmlinuz" {
		t.Fatalf("unexpected kernel url: %#v", spec)
	}
	if spec.InitrdURL != "http://boot.test/custom/initrd" {
		t.Fatalf("unexpected initrd url: %#v", spec)
	}
	if !strings.Contains(spec.KernelParams, "url=http://boot.test/images/9/file") || !strings.Contains(spec.KernelParams, "ds=nocloud-net;s=http://boot.test/metadata/by-token/metadata-token/") {
		t.Fatalf("expected expanded kernel params: %#v", spec)
	}
}

func TestInstallerBootSpecLetsDeploymentVariablesOverrideImageTags(t *testing.T) {
	service := BootService{baseURL: "http://boot.test"}
	image := models.Image{
		ID:   3,
		Tags: datatypes.JSON([]byte(`{"kernel_url":"image/vmlinuz","initrd_url":"image/initrd","kernel_params":"image params"}`)),
	}
	deployment := models.Deployment{
		ImageID: 3,
		Variables: datatypes.JSON([]byte(`{
			"kernel_url": "http://mirror.example.com/vmlinuz",
			"initrd_url": "deployment/initrd",
			"kernel_params": "deployment {image_url}"
		}`)),
	}

	spec := service.installerBootSpec(image, deployment, models.MetadataToken{Token: "token"})
	if spec.KernelURL != "http://mirror.example.com/vmlinuz" {
		t.Fatalf("expected deployment kernel url override: %#v", spec)
	}
	if spec.InitrdURL != "http://boot.test/boot-assets/deployment/initrd" {
		t.Fatalf("expected deployment initrd url override: %#v", spec)
	}
	if spec.KernelParams != "deployment http://boot.test/images/3/file" {
		t.Fatalf("expected deployment kernel params override: %#v", spec)
	}
}

func TestLinuxInstallerScriptBootsKernelAndInitrd(t *testing.T) {
	script := (BootService{}).LinuxInstallerScript()
	for _, needle := range []string{
		"isset ${kernel-url}",
		"isset ${initrd-url}",
		"kernel ${kernel-url} ${kernel-params} || goto boot_failed",
		"initrd ${initrd-url} || goto boot_failed",
		"boot || goto boot_failed",
		"Missing kernel-url or initrd-url",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("expected installer script to contain %q: %s", needle, script)
		}
	}
}

func metadataTokenTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.MetadataToken{}); err != nil {
		t.Fatal(err)
	}
	return db
}
