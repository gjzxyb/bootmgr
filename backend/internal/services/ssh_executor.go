package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"gorm.io/gorm"
)

type RemoteCommandResult struct {
	Stdout           string
	Stderr           string
	ExitCode         int
	FailureStage     string
	HostKeyPolicy    string
	HostKeyVerified  bool
	HostKeyAlgorithm string
	HostKeySHA256    string
	HostKeyHost      string
	HostKeyRemote    string
}

const DefaultSSHCheckCommand = "printf 'ok '; hostname; id -un; uname -srm"

type SSHCommandExecutor struct {
	db             *gorm.DB
	credentials    CredentialService
	hostKeyPolicy  string
	knownHostsPath string
	connectTimeout time.Duration
}

func NewSSHCommandExecutor(db *gorm.DB, cfg config.Config) SSHCommandExecutor {
	timeout := cfg.SSHConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return SSHCommandExecutor{
		db:             db,
		credentials:    NewCredentialService(db, cfg),
		hostKeyPolicy:  strings.ToLower(strings.TrimSpace(cfg.SSHHostKeyPolicy)),
		knownHostsPath: strings.TrimSpace(cfg.SSHKnownHostsPath),
		connectTimeout: timeout,
	}
}

func (e SSHCommandExecutor) TargetForServer(serverID uint) (models.SSHAccess, error) {
	var target models.SSHAccess
	if err := e.db.Where("server_id = ?", serverID).First(&target).Error; err != nil {
		return target, err
	}
	return target, nil
}

func (e SSHCommandExecutor) RunForServer(ctx context.Context, serverID uint, command string) (RemoteCommandResult, error) {
	target, err := e.TargetForServer(serverID)
	if err != nil {
		return e.failureResult("lookup", nil), err
	}
	return e.Run(ctx, target, command)
}

func (e SSHCommandExecutor) RunScriptForServer(ctx context.Context, serverID uint, script string) (RemoteCommandResult, error) {
	target, err := e.TargetForServer(serverID)
	if err != nil {
		return e.failureResult("lookup", nil), err
	}
	return e.RunScript(ctx, target, script)
}

func (e SSHCommandExecutor) Check(ctx context.Context, serverID uint) (models.SSHAccess, error) {
	target, _, err := e.CheckWithCommand(ctx, serverID, DefaultSSHCheckCommand)
	return target, err
}

func (e SSHCommandExecutor) CheckWithCommand(ctx context.Context, serverID uint, command string) (models.SSHAccess, RemoteCommandResult, error) {
	target, err := e.TargetForServer(serverID)
	if err != nil {
		return target, e.failureResult("lookup", nil), err
	}
	if strings.TrimSpace(command) == "" {
		command = DefaultSSHCheckCommand
	}
	result, err := e.Run(ctx, target, command)
	now := time.Now().UTC()
	if err != nil || result.ExitCode != 0 {
		_ = e.db.Model(&target).Updates(map[string]any{"status": "error", "last_checked_at": now}).Error
		target.Status = "error"
		target.LastCheckedAt = &now
		if err != nil {
			return target, result, err
		}
		return target, result, fmt.Errorf("ssh check command exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	_ = e.db.Model(&target).Updates(map[string]any{"status": "ok", "last_checked_at": now}).Error
	target.Status = "ok"
	target.LastCheckedAt = &now
	return target, result, nil
}

func (e SSHCommandExecutor) Run(ctx context.Context, target models.SSHAccess, command string) (RemoteCommandResult, error) {
	if strings.TrimSpace(command) == "" {
		return e.failureResult("request", nil), errors.New("ssh command cannot be empty")
	}
	return e.runSession(ctx, target, func(session *ssh.Session, stdout, stderr *bytes.Buffer) error {
		return session.Run(command)
	})
}

func (e SSHCommandExecutor) RunScript(ctx context.Context, target models.SSHAccess, script string) (RemoteCommandResult, error) {
	if strings.TrimSpace(script) == "" {
		return e.failureResult("request", nil), errors.New("ssh script cannot be empty")
	}
	return e.runSession(ctx, target, func(session *ssh.Session, stdout, stderr *bytes.Buffer) error {
		stdin, err := session.StdinPipe()
		if err != nil {
			return err
		}
		errCh := make(chan error, 1)
		go func() {
			_, copyErr := io.Copy(stdin, strings.NewReader(script))
			closeErr := stdin.Close()
			if copyErr != nil {
				errCh <- copyErr
				return
			}
			errCh <- closeErr
		}()
		runErr := session.Run("sh -s")
		if pipeErr := <-errCh; pipeErr != nil && runErr == nil {
			return pipeErr
		}
		return runErr
	})
}

func (e SSHCommandExecutor) runSession(ctx context.Context, target models.SSHAccess, run func(*ssh.Session, *bytes.Buffer, *bytes.Buffer) error) (RemoteCommandResult, error) {
	cfg, address, hostKeyProof, err := e.clientConfig(target)
	if err != nil {
		return e.failureResult("config", hostKeyProof), err
	}
	dialer := net.Dialer{Timeout: e.connectTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return e.failureResult("dial", hostKeyProof), err
	}
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		return e.failureResult("handshake", hostKeyProof), err
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return e.failureResult("session", hostKeyProof), err
	}
	defer session.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	runErr := run(session, &stdout, &stderr)
	result := RemoteCommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0}
	applyHostKeyProof(&result, hostKeyProof)
	if runErr == nil {
		return result, nil
	}
	result.ExitCode = 1
	result.FailureStage = "command"
	var exitErr *ssh.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitStatus()
	}
	return result, runErr
}

type sshHostKeyProof struct {
	Policy    string
	Verified  bool
	Algorithm string
	SHA256    string
	Host      string
	Remote    string
}

func (e SSHCommandExecutor) normalizedHostKeyPolicy() string {
	policy := strings.ToLower(strings.TrimSpace(e.hostKeyPolicy))
	if policy == "" {
		return "insecure_ignore"
	}
	return policy
}

func (e SSHCommandExecutor) newHostKeyProof() *sshHostKeyProof {
	return &sshHostKeyProof{Policy: e.normalizedHostKeyPolicy()}
}

func (e SSHCommandExecutor) failureResult(stage string, proof *sshHostKeyProof) RemoteCommandResult {
	result := RemoteCommandResult{ExitCode: -1, FailureStage: stage, HostKeyPolicy: e.normalizedHostKeyPolicy()}
	applyHostKeyProof(&result, proof)
	return result
}

func applyHostKeyProof(result *RemoteCommandResult, proof *sshHostKeyProof) {
	if result == nil || proof == nil {
		return
	}
	result.HostKeyPolicy = proof.Policy
	result.HostKeyVerified = proof.Verified
	result.HostKeyAlgorithm = proof.Algorithm
	result.HostKeySHA256 = proof.SHA256
	result.HostKeyHost = proof.Host
	result.HostKeyRemote = proof.Remote
}

func (e SSHCommandExecutor) clientConfig(target models.SSHAccess) (*ssh.ClientConfig, string, *sshHostKeyProof, error) {
	proof := e.newHostKeyProof()
	host := strings.TrimSpace(target.Host)
	if host == "" {
		return nil, "", proof, errors.New("ssh target host is not configured")
	}
	port := target.Port
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return nil, "", proof, errors.New("ssh target port is invalid")
	}
	username := strings.TrimSpace(target.Username)
	if username == "" {
		return nil, "", proof, errors.New("ssh target username is not configured")
	}
	hostKeyCallback, proof, err := e.hostKeyCallback()
	if err != nil {
		return nil, "", proof, err
	}
	auth, err := e.authMethods(target)
	if err != nil {
		return nil, "", proof, err
	}
	return &ssh.ClientConfig{
		User:            username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		Timeout:         e.connectTimeout,
	}, net.JoinHostPort(host, strconv.Itoa(port)), proof, nil
}

func (e SSHCommandExecutor) authMethods(target models.SSHAccess) ([]ssh.AuthMethod, error) {
	authType := strings.ToLower(strings.TrimSpace(target.AuthType))
	if authType == "" {
		authType = "password"
	}
	if strings.TrimSpace(target.CredentialRef) == "" {
		return nil, fmt.Errorf("ssh %s credential is not configured", authType)
	}
	credentialID, err := parseUint(target.CredentialRef)
	if err != nil {
		return nil, fmt.Errorf("ssh credential reference is invalid: %w", err)
	}
	secret, err := e.credentials.Secret(credentialID)
	if err != nil {
		return nil, fmt.Errorf("load ssh credential: %w", err)
	}
	switch authType {
	case "password":
		return []ssh.AuthMethod{ssh.Password(secret), ssh.KeyboardInteractive(passwordKeyboardInteractive(secret))}, nil
	case "private_key":
		signer, err := ssh.ParsePrivateKey([]byte(secret))
		if err != nil {
			return nil, fmt.Errorf("parse ssh private key credential: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("unsupported ssh auth_type %q", target.AuthType)
	}
}

func (e SSHCommandExecutor) hostKeyCallback() (ssh.HostKeyCallback, *sshHostKeyProof, error) {
	policy := e.normalizedHostKeyPolicy()
	proof := e.newHostKeyProof()
	record := func(hostname string, remote net.Addr, key ssh.PublicKey, verified bool) {
		proof.Host = hostname
		if remote != nil {
			proof.Remote = remote.String()
		}
		if key != nil {
			proof.Algorithm = key.Type()
			proof.SHA256 = ssh.FingerprintSHA256(key)
		}
		proof.Verified = verified
	}
	switch policy {
	case "", "insecure_ignore":
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			record(hostname, remote, key, false)
			return nil
		}, proof, nil
	case "known_hosts":
		if strings.TrimSpace(e.knownHostsPath) == "" {
			return nil, proof, errors.New("SSH_KNOWN_HOSTS_PATH is required when SSH_HOST_KEY_POLICY=known_hosts")
		}
		callback, err := knownhosts.New(e.knownHostsPath)
		if err != nil {
			return nil, proof, fmt.Errorf("load SSH known_hosts file: %w", err)
		}
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			err := callback(hostname, remote, key)
			record(hostname, remote, key, err == nil)
			return err
		}, proof, nil
	default:
		return nil, proof, fmt.Errorf("unsupported SSH_HOST_KEY_POLICY %q", e.hostKeyPolicy)
	}
}

func passwordKeyboardInteractive(secret string) ssh.KeyboardInteractiveChallenge {
	return func(user, instruction string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range answers {
			answers[i] = secret
		}
		return answers, nil
	}
}
