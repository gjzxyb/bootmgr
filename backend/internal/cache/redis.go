package cache

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
)

type RedisClient struct {
	addr     string
	password string
	db       int
}

func NewRedisClient(cfg config.Config) RedisClient {
	return RedisClient{addr: cfg.RedisAddr, password: cfg.RedisPassword, db: cfg.RedisDB}
}

func (c RedisClient) Enabled() bool {
	return c.addr != ""
}

func (c RedisClient) Ping(ctx context.Context) error {
	if !c.Enabled() {
		return nil
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(conn)
	if c.password != "" {
		if err := c.command(conn, reader, "AUTH", c.password); err != nil {
			return err
		}
	}
	if c.db > 0 {
		if err := c.command(conn, reader, "SELECT", strconv.Itoa(c.db)); err != nil {
			return err
		}
	}
	return c.command(conn, reader, "PING")
}

func (c RedisClient) command(conn net.Conn, reader *bufio.Reader, parts ...string) error {
	var builder strings.Builder
	builder.WriteString("*")
	builder.WriteString(strconv.Itoa(len(parts)))
	builder.WriteString("\r\n")
	for _, part := range parts {
		builder.WriteString("$")
		builder.WriteString(strconv.Itoa(len(part)))
		builder.WriteString("\r\n")
		builder.WriteString(part)
		builder.WriteString("\r\n")
	}
	if _, err := conn.Write([]byte(builder.String())); err != nil {
		return err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "-") {
		return fmt.Errorf("redis command failed: %s", strings.TrimPrefix(line, "-"))
	}
	return nil
}
