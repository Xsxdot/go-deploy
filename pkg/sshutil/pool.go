package sshutil

import (
	"context"
	"sync"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"

	"golang.org/x/crypto/ssh"
)

// DialFunc 建立 SSH 连接
type DialFunc func(ctx context.Context, host *core.HostTarget) (*ssh.Client, error)

type pooledClient struct {
	client   *ssh.Client
	host     *core.HostTarget
	lastUsed time.Time
}

// Pool 管理 SSH 客户端会话池，按 targetKey 复用连接
type Pool struct {
	mu            sync.RWMutex
	clients       map[string]*pooledClient
	dial          DialFunc
	keepAlive     time.Duration
	stopKeepAlive chan struct{}
	closeOnce     sync.Once
	logger        Logger
}

// NewPool 创建会话池，dial 用于建立新连接，keepAlive 为后台保活间隔，0 表示不启用，logger 可选
func NewPool(dial DialFunc, keepAlive time.Duration, logger Logger) *Pool {
	if logger == nil {
		logger = nopLogger{}
	}
	p := &Pool{
		clients:       make(map[string]*pooledClient),
		dial:          dial,
		keepAlive:     keepAlive,
		stopKeepAlive: make(chan struct{}),
		logger:        logger,
	}
	if keepAlive > 0 {
		go p.keepAliveLoop()
	}
	return p
}

func (p *Pool) poolKey(host *core.HostTarget) string {
	if host.ResourceID != "" {
		return host.ResourceID
	}
	return host.Addr + "@" + host.User
}

// GetOrCreate 获取或创建目标主机的 SSH 客户端；发命令前会检查有效性，无效则重连
func (p *Pool) GetOrCreate(ctx context.Context, host *core.HostTarget) (*ssh.Client, error) {
	key := p.poolKey(host)

	p.mu.Lock()
	pc, ok := p.clients[key]
	if ok {
		if sessionValid(pc.client) {
			pc.lastUsed = time.Now()
			client := pc.client
			p.mu.Unlock()
			return client, nil
		}
		delete(p.clients, key)
		_ = pc.client.Close()
	}
	p.mu.Unlock()

	client, err := p.dial(ctx, host)
	if err != nil {
		p.logger.Error("GetOrCreate 拨号失败", "host", host.Addr, "err", err)
		return nil, err
	}

	p.mu.Lock()
	p.clients[key] = &pooledClient{client: client, host: host, lastUsed: time.Now()}
	p.mu.Unlock()

	return client, nil
}

// sessionValid 检查 SSH 连接是否有效
func sessionValid(client *ssh.Client) bool {
	if client == nil {
		return false
	}
	_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

func (p *Pool) keepAliveLoop() {
	ticker := time.NewTicker(p.keepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopKeepAlive:
			return
		case <-ticker.C:
			p.sendKeepAliveToIdle()
		}
	}
}

func (p *Pool) sendKeepAliveToIdle() {
	p.mu.RLock()
	candidates := make([]*pooledClient, 0, len(p.clients))
	for _, pc := range p.clients {
		if time.Since(pc.lastUsed) > p.keepAlive/2 {
			candidates = append(candidates, pc)
		}
	}
	p.mu.RUnlock()

	for _, pc := range candidates {
		if sessionValid(pc.client) {
			p.mu.Lock()
			if c, ok := p.clients[p.poolKey(pc.host)]; ok && c == pc {
				c.lastUsed = time.Now()
			}
			p.mu.Unlock()
		} else {
			p.mu.Lock()
			key := p.poolKey(pc.host)
			if p.clients[key] == pc {
				delete(p.clients, key)
				p.logger.Error("连接无效已删除", "host", pc.host.Addr)
				_ = pc.client.Close()
			}
			p.mu.Unlock()
		}
	}
}

// Close 关闭池内所有连接
func (p *Pool) Close() error {
	p.closeOnce.Do(func() {
		if p.keepAlive > 0 {
			close(p.stopKeepAlive)
		}
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, pc := range p.clients {
		_ = pc.client.Close()
		delete(p.clients, key)
	}
	return nil
}
