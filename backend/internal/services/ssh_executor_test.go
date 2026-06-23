package services

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"gorm.io/gorm"
)

func TestSSHCommandExecutorRunsCommandAgainstRealSSHServer(t *testing.T) {
	addr, _, closeServer := startTestSSHServer(t, "tester", "secret-password")
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
	if result.HostKeyPolicy != "insecure_ignore" || result.HostKeyVerified || result.HostKeyAlgorithm == "" || !strings.HasPrefix(result.HostKeySHA256, "SHA256:") {
		t.Fatalf("expected insecure host key proof to be captured without verification: %#v", result)
	}

	checked, err := executor.Check(ctx, 42)
	if err != nil {
		t.Fatalf("check ssh target: %v", err)
	}
	if checked.Status != "ok" || checked.LastCheckedAt == nil {
		t.Fatalf("expected ssh access status ok with timestamp: %#v", checked)
	}

	checked, probe, err := executor.CheckWithCommand(ctx, 42, "printf lab-proof")
	if err != nil {
		t.Fatalf("check ssh target with custom probe: %v", err)
	}
	if checked.Status != "ok" || probe.ExitCode != 0 || !strings.Contains(probe.Stdout, "lab-proof") {
		t.Fatalf("expected custom ssh probe output and ok status: checked=%#v probe=%#v", checked, probe)
	}
}

func TestSSHCommandExecutorVerifiesKnownHosts(t *testing.T) {
	addr, hostKey, closeServer := startTestSSHServer(t, "tester", "secret-password")
	defer closeServer()

	knownHostsPath := t.TempDir() + "/known_hosts"
	if err := os.WriteFile(knownHostsPath, []byte(knownhosts.Line([]string{addr}, hostKey)+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	db := newSSHExecutorTestDB(t)
	cfg := config.Config{CredentialKey: "ssh-executor-test-key", SSHHostKeyPolicy: "known_hosts", SSHKnownHostsPath: knownHostsPath, SSHConnectTimeout: 5 * time.Second}
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
	checked, probe, err := executor.CheckWithCommand(ctx, 42, "printf lab-proof")
	if err != nil {
		t.Fatalf("known_hosts ssh check should pass: %v", err)
	}
	if checked.Status != "ok" || probe.ExitCode != 0 || !strings.Contains(probe.Stdout, "lab-proof") {
		t.Fatalf("expected known_hosts ssh check output: checked=%#v probe=%#v", checked, probe)
	}
	if probe.HostKeyPolicy != "known_hosts" || !probe.HostKeyVerified || probe.HostKeyAlgorithm == "" || !strings.HasPrefix(probe.HostKeySHA256, "SHA256:") {
		t.Fatalf("expected verified known_hosts proof in SSH result: %#v", probe)
	}
}

func TestSSHCommandExecutorReturnsProofForKnownHostsConfigError(t *testing.T) {
	db := newSSHExecutorTestDB(t)
	cfg := config.Config{CredentialKey: "ssh-executor-test-key", SSHHostKeyPolicy: "known_hosts", SSHConnectTimeout: time.Second}
	executor := NewSSHCommandExecutor(db, cfg)

	result, err := executor.Run(context.Background(), models.SSHAccess{Host: "127.0.0.1", Port: 22, Username: "tester", AuthType: "password"}, "true")
	if err == nil || !strings.Contains(err.Error(), "SSH_KNOWN_HOSTS_PATH") {
		t.Fatalf("expected missing known_hosts error, got result=%#v err=%v", result, err)
	}
	if result.ExitCode != -1 || result.FailureStage != "config" || result.HostKeyPolicy != "known_hosts" {
		t.Fatalf("expected config failure with known_hosts proof context, got %#v", result)
	}
}

func TestSSHCommandExecutorReturnsProofForKnownHostsMismatch(t *testing.T) {
	addr, _, closeServer := startTestSSHServer(t, "tester", "secret-password")
	defer closeServer()

	_, wrongHostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate wrong host key: %v", err)
	}
	wrongSigner, err := ssh.NewSignerFromKey(wrongHostKey)
	if err != nil {
		t.Fatalf("create wrong host signer: %v", err)
	}
	knownHostsPath := t.TempDir() + "/known_hosts"
	if err := os.WriteFile(knownHostsPath, []byte(knownhosts.Line([]string{addr}, wrongSigner.PublicKey())+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	db := newSSHExecutorTestDB(t)
	cfg := config.Config{CredentialKey: "ssh-executor-test-key", SSHHostKeyPolicy: "known_hosts", SSHKnownHostsPath: knownHostsPath, SSHConnectTimeout: 5 * time.Second}
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
	checked, probe, err := executor.CheckWithCommand(ctx, 42, "printf lab-proof")
	if err == nil {
		t.Fatalf("expected known_hosts mismatch to fail")
	}
	if checked.Status != "error" || checked.LastCheckedAt == nil {
		t.Fatalf("expected failed SSH check to mark access error with timestamp: %#v", checked)
	}
	if probe.ExitCode != -1 || probe.FailureStage != "handshake" || probe.HostKeyPolicy != "known_hosts" || probe.HostKeyVerified {
		t.Fatalf("expected handshake failure with unverified known_hosts proof, got %#v", probe)
	}
	if probe.HostKeyAlgorithm == "" || !strings.HasPrefix(probe.HostKeySHA256, "SHA256:") || probe.HostKeyHost == "" || probe.HostKeyRemote == "" {
		t.Fatalf("expected mismatched host key proof fields, got %#v", probe)
	}
}

func startTestSSHServer(t *testing.T, username string, password string) (string, ssh.PublicKey, func()) {
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
	return listener.Addr().String(), signer.PublicKey(), closeFn
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
			case DefaultSSHCheckCommand:
				_, _ = channel.Write([]byte("ok lab-ssh\ntester\nLinux lab-ssh 6.1.0 x86_64\n"))
				sendTestSSHExitStatus(channel, 0)
			case "printf 'ok '; hostname":
				_, _ = channel.Write([]byte("ok lab-ssh\n"))
				sendTestSSHExitStatus(channel, 0)
			case "printf lab-proof":
				_, _ = channel.Write([]byte("lab-proof"))
				sendTestSSHExitStatus(channel, 0)
			case "printf 'SSH terminal session opened on '; hostname; printf 'user='; id -un; uname -a":
				_, _ = channel.Write([]byte("SSH terminal session opened on lab-ssh\nuser=tester\nLinux lab-ssh 6.1.0 x86_64\n"))
				sendTestSSHExitStatus(channel, 0)
			case "printf terminal-ok":
				_, _ = channel.Write([]byte("terminal-ok"))
				sendTestSSHExitStatus(channel, 0)
			case "sh -s":
				script, _ := io.ReadAll(channel)
				switch strings.TrimSpace(string(script)) {
				case "printf script-ok":
					_, _ = channel.Write([]byte("script-ok"))
					sendTestSSHExitStatus(channel, 0)
				case "printf script-error >&2\nexit 7":
					_, _ = channel.Stderr().Write([]byte("script-error"))
					sendTestSSHExitStatus(channel, 7)
				default:
					_, _ = channel.Stderr().Write([]byte(fmt.Sprintf("unsupported script: %s\n", strings.TrimSpace(string(script)))))
					sendTestSSHExitStatus(channel, 127)
				}
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
