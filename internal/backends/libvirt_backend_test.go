package backends

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c12s/wormhole/internal/config"
)

func TestStaticAddress(t *testing.T) {
	cases := []struct {
		name, want string
		offset     int
		wantErr    bool
	}{
		{"host-1", "192.168.122.51", 50, false},
		{"node-1", "192.168.122.61", 60, false},
		{"node-3", "192.168.122.63", 60, false},
		{"node", "", 50, true},
		{"node-250", "", 50, true},
	}
	for _, c := range cases {
		got, err := staticAddress(c.name, "192.168.122.", c.offset)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("staticAddress(%q, %d) = %q, %v; want %q, err=%v", c.name, c.offset, got, err, c.want, c.wantErr)
		}
	}
}

func TestDeterministicMACUniquePerAddress(t *testing.T) {
	a := deterministicMAC("192.168.122.51")
	b := deterministicMAC("192.168.122.61")
	if a == b {
		t.Fatalf("distinct addresses share MAC %s", a)
	}
	if a != "52:54:00:a8:7a:33" {
		t.Errorf("unexpected MAC %s", a)
	}
}

func TestNamesDefaultsToAllConfiguredVMs(t *testing.T) {
	l := &LibvirtBackend{conf: &config.Config{NameBase: "node", VMCount: 2}}
	got := l.names(nil)
	if len(got) != 2 || got[0] != "node-1" || got[1] != "node-2" {
		t.Errorf("names(nil) = %v", got)
	}
	if got := l.names([]string{"x-9"}); len(got) != 1 || got[0] != "x-9" {
		t.Errorf("explicit names not kept: %v", got)
	}
}

func TestAddressOwnerDetectsCollision(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "host-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "version: 1\nconfig:\n  - type: physical\n    subnets:\n      - type: static\n        address: 192.168.122.51\n"
	if err := os.WriteFile(filepath.Join(dir, "host-1", "network-config"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &LibvirtBackend{conf: &config.Config{StateDir: dir}}
	if owner := l.addressOwner("192.168.122.51", "node-1"); owner != "host-1" {
		t.Errorf("collision not detected, owner=%q", owner)
	}
	if owner := l.addressOwner("192.168.122.51", "host-1"); owner != "" {
		t.Errorf("own seed reported as collision")
	}
	if owner := l.addressOwner("192.168.122.61", "node-1"); owner != "" {
		t.Errorf("free address reported taken by %q", owner)
	}
}

func TestNodeEnvLinesPerNodeOnly(t *testing.T) {
	lines := nodeEnvLines("", map[string]string{"VM_NETWORK": "c12s-vms", "VM_IP_BASE": "192.168.123"})
	got := strings.Join(lines, "\n")
	if strings.Contains(got, "NATS_ADDRESS") {
		t.Fatalf("no NATS override configured, but seed sets it:\n%s", got)
	}
	for _, want := range []string{"VM_IP_BASE=192.168.123", "VM_NETWORK=c12s-vms"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(strings.Join(nodeEnvLines("10.0.0.1:4222", nil), "\n"), "NATS_ADDRESS=10.0.0.1:4222") {
		t.Fatal("NATS override not written")
	}
}

func writeKey(t *testing.T, dir, name, key string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(key+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveLoginDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeKey(t, filepath.Join(home, ".ssh"), "id_ed25519.pub", "ssh-ed25519 AAAAhome me@lab")

	login, err := resolveLogin(nil)
	if err != nil {
		t.Fatal(err)
	}
	if login.user != "c12s" || login.password != "c12s" || len(login.keys) != 1 || login.keys[0] != "ssh-ed25519 AAAAhome me@lab" {
		t.Fatalf("defaults: %+v", login)
	}
	userData := renderUserData(login, "", "")
	for _, want := range []string{"name: c12s", `password: "c12s"`, "ssh_pwauth: true", "lock_passwd: false", "- ssh-ed25519 AAAAhome me@lab"} {
		if !strings.Contains(userData, want) {
			t.Errorf("default user-data missing %q:\n%s", want, userData)
		}
	}
}

func TestResolveLoginCustomKeysOnly(t *testing.T) {
	dir := t.TempDir()
	keyFile := writeKey(t, dir, "ops.pub", "ssh-ed25519 AAAAfile ops@lab")
	empty := ""
	login, err := resolveLogin(&config.Login{
		User:     "ops",
		Password: &empty,
		SSHKeys:  []string{keyFile, "ssh-rsa AAAAliteral other@lab"},
	})
	if err != nil {
		t.Fatal(err)
	}
	userData := renderUserData(login, "", "")
	for _, want := range []string{"name: ops", "lock_passwd: true", "ssh_pwauth: false", "- ssh-ed25519 AAAAfile ops@lab", "- ssh-rsa AAAAliteral other@lab"} {
		if !strings.Contains(userData, want) {
			t.Errorf("user-data missing %q:\n%s", want, userData)
		}
	}
	if strings.Contains(userData, "chpasswd") {
		t.Errorf("no password set, but user-data sets one:\n%s", userData)
	}
}

func TestResolveLoginRejectsNoWayIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.ssh keys
	empty := ""
	if _, err := resolveLogin(&config.Login{Password: &empty}); err == nil {
		t.Fatal("no password and no ssh keys must be rejected")
	}
	if _, err := resolveLogin(&config.Login{User: "Bad User"}); err == nil {
		t.Fatal("invalid user name must be rejected")
	}
	if _, err := resolveLogin(&config.Login{SSHKeys: []string{"/nope/missing.pub"}}); err == nil {
		t.Fatal("missing key file must be rejected")
	}
}
