package services

import (
	"strings"
	"testing"
	"time"

	"baremetal-platform/backend/internal/models"

	"github.com/glebarez/sqlite"
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
