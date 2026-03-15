package sshutil

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Xsxdot/go-deploy/internal/core"
)

var integrationHost = &core.HostTarget{
	ResourceID: "integration-test",
	Addr:       "192.168.0.2",
	User:       "root",
	Auth:       map[string]string{"keyPath": "~/.ssh/id_rsa"},
}

func TestAsHostTarget(t *testing.T) {
	host := &core.HostTarget{ResourceID: "h1", Addr: "192.168.1.1", User: "root"}
	if h, ok := AsHostTarget(host); !ok || h != host {
		t.Errorf("AsHostTarget(host) = %v, %v; want host, true", h, ok)
	}

	k8s := &core.K8sTarget{ResourceID: "k1", Context: "ctx", Namespace: "ns"}
	if _, ok := AsHostTarget(k8s); ok {
		t.Error("AsHostTarget(k8s) should return false")
	}

	if _, ok := AsHostTarget(nil); ok {
		t.Error("AsHostTarget(nil) should return false")
	}
}

func TestIsHostTarget(t *testing.T) {
	if !IsHostTarget(&core.HostTarget{}) {
		t.Error("IsHostTarget(HostTarget) should be true")
	}
	if IsHostTarget(&core.K8sTarget{}) {
		t.Error("IsHostTarget(K8sTarget) should be false")
	}
}

func TestExecutor_Run_ErrNotHostTarget(t *testing.T) {
	exec := New(nil)
	defer exec.Close()

	_, _, _, err := exec.Run(context.Background(), &core.K8sTarget{ResourceID: "k1"}, "echo ok", nil)
	if err != ErrNotHostTarget {
		t.Errorf("Run(K8sTarget) err = %v; want ErrNotHostTarget", err)
	}
}

func TestExecutor_Stream_ErrNotHostTarget(t *testing.T) {
	exec := New(nil)
	defer exec.Close()

	err := exec.Stream(context.Background(), &core.K8sTarget{ResourceID: "k1"}, "echo ok", nil)
	if err != ErrNotHostTarget {
		t.Errorf("Stream(K8sTarget) err = %v; want ErrNotHostTarget", err)
	}
}

func TestExecutor_Run_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	exec := New(nil)
	defer exec.Close()

	stdout, stderr, code, err := exec.Run(context.Background(), integrationHost, "echo hello", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("code = %d; want 0", code)
	}
	if !strings.Contains(stdout, "hello") {
		t.Errorf("stdout = %q; want to contain 'hello'", stdout)
	}
	if stderr != "" {
		t.Logf("stderr: %s", stderr)
	}
}

func TestExecutor_Stream_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	exec := New(nil)
	defer exec.Close()

	var buf bytes.Buffer
	err := exec.Stream(context.Background(), integrationHost, "echo hello", &StreamOptions{Stdout: &buf})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("buf = %q; want to contain 'hello'", buf.String())
	}
}
