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
	"gorm.io/gorm"
)

type RemoteCommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type SSHCommandExecutor struct {
	db             *gorm.DB
	credentials    CredentialService
	hostKeyPolicy  string
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
		return RemoteCommandResult{ExitCode: -1}, err
	}
	return e.Run(ctx, target, command)
}

func (e SSHCommandExecutor) RunScriptForServer(ctx context.Context, serverID uint, script string) (RemoteCommandResult, error) {
	target, err := e.TargetForServer(serverID)
	if err != nil {
		return RemoteCommandResult{ExitCode: -1}, err
	}
	return e.RunScript(ctx, target, script)
}

func (e SSHCommandExecutor) Run(ctx context.Context, target models.SSHAccess, command string) (RemoteCommandResult, error) {
	if strings.TrimSpace(command) == "" {
		return RemoteCommandResult{ExitCode: -1}, errors.New("ssh command cannot be empty")
	}
	return e.runSession(ctx, target, func(session *ssh.Session, stdout, stderr *bytes.Buffer) error {
		return session.Run(command)
	})
}

func (e SSHCommandExecutor) RunScript(ctx context.Context, target models.SSHAccess, script string) (RemoteCommandResult, error) {
	if strings.TrimSpace(script) == "" {
		return RemoteCommandResult{ExitCode: -1}, errors.New("ssh script cannot be empty")
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
	cfg, address, err := e.clientConfig(target)
	if err != nil {
		return RemoteCommandResult{ExitCode: -1}, err
	}
	dialer := net.Dialer{Timeout: e.connectTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return RemoteCommandResult{ExitCode: -1}, err
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
		return RemoteCommandResult{ExitCode: -1}, err
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return RemoteCommandResult{ExitCode: -1}, err
	}
	defer session.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	runErr := run(session, &stdout, &stderr)
	result := RemoteCommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0}
	if runErr == nil {
		return result, nil
	}
	result.ExitCode = 1
	var exitErr *ssh.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitStatus()
	}
	return result, runErr
}

func (e SSHCommandExecutor) clientConfig(target models.SSHAccess) (*ssh.ClientConfig, string, error) {
	host := strings.TrimSpace(target.Host)
	if host == "" {
		return nil, "", errors.New("ssh target host is not configured")
	}
	port := target.Port
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return nil, "", errors.New("ssh target port is invalid")
	}
	username := strings.TrimSpace(target.Username)
	if username == "" {
		return nil, "", errors.New("ssh target username is not configured")
	}
	auth, err := e.authMethods(target)
	if err != nil {
		return nil, "", err
	}
	hostKeyCallback, err := e.hostKeyCallback()
	if err != nil {
		return nil, "", err
	}
	return &ssh.ClientConfig{
		User:            username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		Timeout:         e.connectTimeout,
	}, net.JoinHostPort(host, strconv.Itoa(port)), nil
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

func (e SSHCommandExecutor) hostKeyCallback() (ssh.HostKeyCallback, error) {
	switch strings.ToLower(strings.TrimSpace(e.hostKeyPolicy)) {
	case "", "insecure_ignore":
		return ssh.InsecureIgnoreHostKey(), nil
	default:
		return nil, fmt.Errorf("unsupported SSH_HOST_KEY_POLICY %q", e.hostKeyPolicy)
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
