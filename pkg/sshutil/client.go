package sshutil

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

// Dial 根据 HostTarget 建立 SSH 连接，支持 keyPath/password、Proxy（SOCKS5）。
// 若需堡垒机跳转，请使用 NewDialFunc 工厂方法，或直接调用 DialViaBastion。
func Dial(ctx context.Context, host *core.HostTarget) (*ssh.Client, error) {
	cfg, err := buildSSHConfig(host)
	if err != nil {
		return nil, fmt.Errorf("build ssh config: %w", err)
	}

	addr := normalizeAddr(host.Addr)
	var conn net.Conn

	if host.Proxy != "" {
		conn, err = dialViaProxy(ctx, host.Proxy, addr)
	} else {
		var d net.Dialer
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	clientConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}

	return ssh.NewClient(clientConn, chans, reqs), nil
}

// DialViaBastion 经由堡垒机建立到目标主机的 SSH 连接（ProxyJump）。
// 先与 bastion 建立 SSH 客户端，再通过堡垒机内部 TCP Dial 到目标，最后在隧道上完成 SSH 握手。
func DialViaBastion(ctx context.Context, bastion, host *core.HostTarget) (*ssh.Client, error) {
	// 1. 连接堡垒机
	bastionClient, err := Dial(ctx, bastion)
	if err != nil {
		return nil, fmt.Errorf("dial bastion %s: %w", bastion.Addr, err)
	}

	// 2. 通过堡垒机拨目标内网地址
	targetAddr := normalizeAddr(host.Addr)
	conn, err := bastionClient.Dial("tcp", targetAddr)
	if err != nil {
		_ = bastionClient.Close()
		return nil, fmt.Errorf("bastion dial to target %s: %w", targetAddr, err)
	}

	// 3. 在隧道上建立目标机 SSH 连接
	cfg, err := buildSSHConfig(host)
	if err != nil {
		_ = conn.Close()
		_ = bastionClient.Close()
		return nil, fmt.Errorf("build ssh config for target: %w", err)
	}
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, targetAddr, cfg)
	if err != nil {
		_ = conn.Close()
		_ = bastionClient.Close()
		return nil, fmt.Errorf("ssh handshake via bastion: %w", err)
	}

	return ssh.NewClient(clientConn, chans, reqs), nil
}

// NewDialFunc 返回一个感知 infra 的 DialFunc 工厂。
// 当 host.Bastion 非空时，自动从 infra.Resources 查找堡垒机 HostTarget，经由堡垒机跳转连接。
// 若 infra 为 nil 或堡垒机 resource ID 找不到，则返回错误。
func NewDialFunc(infra *core.InfraConfig) DialFunc {
	return func(ctx context.Context, host *core.HostTarget) (*ssh.Client, error) {
		if host.Bastion == "" || infra == nil {
			return Dial(ctx, host)
		}
		// 查找堡垒机 HostTarget
		bastionTarget, ok := infra.Resources[host.Bastion]
		if !ok {
			return nil, fmt.Errorf("bastion resource %q not found in infra", host.Bastion)
		}
		bastionHost, ok := bastionTarget.(*core.HostTarget)
		if !ok {
			return nil, fmt.Errorf("bastion resource %q is not a HostTarget", host.Bastion)
		}
		return DialViaBastion(ctx, bastionHost, host)
	}
}

func buildSSHConfig(host *core.HostTarget) (*ssh.ClientConfig, error) {
	user := host.User
	if user == "" {
		user = "root"
	}

	authMethods, err := authMethodsFromHost(host)
	if err != nil {
		return nil, err
	}

	return &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}, nil
}

func authMethodsFromHost(host *core.HostTarget) ([]ssh.AuthMethod, error) {
	if host.Auth == nil {
		return nil, fmt.Errorf("auth required: keyPath or password")
	}

	var methods []ssh.AuthMethod

	if keyPath := host.Auth["keyPath"]; keyPath != "" {
		keyPath = expandPath(keyPath)
		key, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read key %s: %w", keyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if pw := host.Auth["password"]; pw != "" {
		methods = append(methods, ssh.Password(pw))
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("auth required: keyPath or password")
	}
	return methods, nil
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func normalizeAddr(addr string) string {
	if addr == "" {
		return "localhost:22"
	}
	if !strings.Contains(addr, ":") {
		return addr + ":22"
	}
	return addr
}

func dialViaProxy(ctx context.Context, proxyURL, targetAddr string) (net.Conn, error) {
	proxyAddr := strings.TrimPrefix(proxyURL, "socks5://")
	proxyAddr = strings.TrimPrefix(proxyAddr, "socks4://")

	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("socks5 dialer: %w", err)
	}

	// proxy.Dial doesn't support context; use DialContext if available
	type contextDialer interface {
		DialContext(ctx context.Context, network, address string) (net.Conn, error)
	}
	if cd, ok := dialer.(contextDialer); ok {
		return cd.DialContext(ctx, "tcp", targetAddr)
	}
	return dialer.Dial("tcp", targetAddr)
}
