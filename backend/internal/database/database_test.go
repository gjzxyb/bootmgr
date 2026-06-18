package database

import (
	"strings"
	"testing"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"
)

func TestServerIdentityIndexesRejectDuplicates(t *testing.T) {
	db, err := Connect(config.Config{
		AppEnv:           "test",
		DBDriver:         "sqlite",
		DatabaseURL:      "file:server-identity-index?mode=memory&cache=shared",
		AdminEmail:       "admin@example.com",
		AdminPassword:    "Admin@123456",
		CredentialKey:    strings.Repeat("c", 32),
		BMCAdapter:       "simulated",
		CollectorMode:    "simulated",
		BootBaseURL:      "http://boot.test",
		ImageStorageDir:  t.TempDir(),
		EnableDemoSeeder: false,
	})
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	first := models.Server{AssetNo: "BM-UNIQUE", Hostname: "identity-a", PrimaryMAC: "52:54:00:11:22:33"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first server: %v", err)
	}
	for name, server := range map[string]models.Server{
		"asset_no":    {AssetNo: "bm-unique", Hostname: "identity-b", PrimaryMAC: "52:54:00:11:22:34"},
		"hostname":    {AssetNo: "BM-OTHER", Hostname: "IDENTITY-A", PrimaryMAC: "52:54:00:11:22:35"},
		"primary_mac": {AssetNo: "BM-OTHER-2", Hostname: "identity-c", PrimaryMAC: "52:54:00:11:22:33"},
	} {
		if err := db.Create(&server).Error; err == nil {
			t.Fatalf("expected duplicate %s to be rejected", name)
		}
	}

	if err := db.Create(&models.Server{Hostname: "blank-identity-a"}).Error; err != nil {
		t.Fatalf("expected blank asset_no and primary_mac to be allowed: %v", err)
	}
	if err := db.Create(&models.Server{Hostname: "blank-identity-b"}).Error; err != nil {
		t.Fatalf("expected repeated blank identity fields to be allowed: %v", err)
	}
}
