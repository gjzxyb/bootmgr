package services

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"gorm.io/gorm"
)

type CredentialService struct {
	db  *gorm.DB
	cfg config.Config
}

func NewCredentialService(db *gorm.DB, cfg config.Config) CredentialService {
	return CredentialService{db: db, cfg: cfg}
}

func (s CredentialService) Store(name, kind, username, secret, createdBy string) (models.Credential, error) {
	if strings.TrimSpace(secret) == "" {
		return models.Credential{}, errors.New("secret is required")
	}
	ciphertext, err := encrypt(s.cfg.CredentialKey, secret)
	if err != nil {
		return models.Credential{}, err
	}
	cred := models.Credential{Name: name, Kind: kind, Username: username, Ciphertext: ciphertext, CreatedBy: createdBy}
	return cred, s.db.Create(&cred).Error
}

func (s CredentialService) Secret(id uint) (string, error) {
	var cred models.Credential
	if err := s.db.First(&cred, id).Error; err != nil {
		return "", err
	}
	return decrypt(s.cfg.CredentialKey, cred.Ciphertext)
}

func encrypt(keyText, plaintext string) (string, error) {
	block, err := aes.NewCipher(key(keyText))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func decrypt(keyText, encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key(keyText))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func key(keyText string) []byte { sum := sha256.Sum256([]byte(keyText)); return sum[:] }
