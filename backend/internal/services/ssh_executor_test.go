package services

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

func TestSSHCommandExecutorRunsCommandAgainstRealSSHServer(t *testing.T) {
	addr, closeServer := startTestSSHServer(t, "tester", "secret-password")
	defer closeServer()

	db := newSSHExecutorTestDB(t)
	cfg := config.Config{CredentialKey: "ssh-executor-test-key", SSHHostKeyPolicy: "insecure_ignore", SSHConnectTimeout: 5 * time.Second}
	credentials := NewCredentialService(db, cfg)
	cred, err := credentials.Store("lab-ssh-password", "ssh", "tester", "secret-password", "tester@example.com")
	if err != nil {
		t.Fatalf("store credential: %v", err)
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split ssh listener address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse ssh listener port: %v", err)
	}
	access := models.SSHAccess{
		ServerID:      42,
		Host:          host,
		Port:          port,
		Username:      "tester",
		AuthType:      "password",
		CredentialRef: strconv.FormatUint(uint64(cred.ID), 10),
		Status:        "configured",
	}
	if err := db.Create(&access).Error; err != nil {
		t.Fatalf("create ssh access: %v", err)
	}

	executor := NewSSHCommandExecutor(db, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := executor.RunForServer(ctx, 42, "printf 'ok '; hostname")
	if err != nil {
		t.Fatalf("run ssh command: %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "ok lab-ssh") {
		t.Fatalf("unexpected ssh command result: %#v", result)
	}

	checked, err := executor.Check(ctx, 42)
	if err != nil {
		t.Fatalf("check ssh target: %v", err)
	}
	if checked.Status != "ok" || checked.LastCheckedAt == nil {
		t.Fatalf("expected ssh access status ok with timestamp: %#v", checked)
	}
}

func startTestSSHServer(t *testing.T, username string, password string) (string, func()) {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ssh host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatalf("create ssh host signer: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if conn.User() == username && string(pass) == password {
				return nil, nil
			}
			return nil, errors.New("permission denied")
		},
	}
	cfg.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ssh test server: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleTestSSHConn(conn, cfg)
		}
	}()
	closeFn := func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	return listener.Addr().String(), closeFn
}

func handleTestSSHConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go handleTestSSHSession(channel, requests)
	}
}

func handleTestSSHSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for req := range requests {
		switch req.Type {
		case "exec":
			var payload struct {
				Command string
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				_ = req.Reply(false, nil)
				return
			}
			_ = req.Reply(true, nil)
			switch strings.TrimSpace(payload.Command) {
			case "printf 'ok '; hostname":
				_, _ = channel.Write([]byte("ok lab-ssh\n"))
				sendTestSSHExitStatus(channel, 0)
			default:
				_, _ = channel.Stderr().Write([]byte(fmt.Sprintf("unsupported command: %s\n", payload.Command)))
				sendTestSSHExitStatus(channel, 127)
			}
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func sendTestSSHExitStatus(channel ssh.Channel, status uint32) {
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct {
		Status uint32
	}{Status: status}))
}

func newSSHExecutorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Credential{}, &models.SSHAccess{}); err != nil {
		t.Fatalf("migrate ssh executor test db: %v", err)
	}
	return db
}
