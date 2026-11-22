package backends

import (
	"fmt"
	"github.com/c12s/wormhole/internal/config"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// NoCloud seed generation: cloud-init picks up user-data/meta-data from a
// volume labelled `cidata` on first boot. The user-data writes
// /var/lib/c12s/node.env — the node's control-plane configuration, which
// persists across every bootc switch and rollback.
//
// When the VM is intended to host VMs (vmNetwork set), the seed also defines
// and starts a dedicated libvirt network inside the guest. It must NOT be the
// libvirt "default" network: the guest's own NIC already sits on the outer
// 192.168.122.0/24, so a second interface on the same subnet would break the
// guest's routing. The reconciler's VM_IP_BASE/VM_NETWORK env point at it.

type seedSpec struct {
	dir         string
	domain      string
	mac         string
	address     string
	gateway     string
	natsAddress string
	extras      map[string]string
	vmNetwork   string
	vmIpBase    string
	login       seedLogin
}

func buildSeed(spec seedSpec) error {
	metaData := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", spec.domain, spec.domain)

	env := strings.Join(nodeEnvLines(spec.natsAddress, spec.extras), "\n")
	writeFiles := fmt.Sprintf(`  - path: /var/lib/c12s/node.env
    permissions: '0644'
    content: |
%s
`, indent(env, "      "))

	runcmd := "  - systemctl enable --now serial-getty@ttyS0.service"
	if spec.vmNetwork != "" {
		writeFiles += fmt.Sprintf(`  - path: /var/lib/c12s/vm-network.xml
    permissions: '0644'
    content: |
%s
`, indent(vmNetworkXML(spec.vmNetwork, spec.vmIpBase), "      "))
		runcmd += fmt.Sprintf(`
  - |
    if command -v virsh >/dev/null 2>&1; then
      virsh net-define /var/lib/c12s/vm-network.xml || true
      virsh net-start %s || true
      virsh net-autostart %s || true
    fi`, spec.vmNetwork, spec.vmNetwork)
	}

	userData := renderUserData(spec.login, writeFiles, runcmd)

	// explicit network config (NoCloud seed, format v1): match the NIC by
	// the deterministic MAC wormhole assigns, static address per VM name.
	// Static (not DHCP): firewalls on the host can drop DHCP broadcasts on
	// the libvirt bridge while unicast passes; static also gives every node a
	// stable, reproducible address.
	networkConfig := fmt.Sprintf(`version: 1
config:
  - type: physical
    name: nic0
    mac_address: "%s"
    subnets:
      - type: static
        address: %s
        netmask: 255.255.255.0
        gateway: %s
`, spec.mac, spec.address, spec.gateway)

	metaPath := filepath.Join(spec.dir, "meta-data")
	userPath := filepath.Join(spec.dir, "user-data")
	netPath := filepath.Join(spec.dir, "network-config")
	if err := os.WriteFile(metaPath, []byte(metaData), 0o644); err != nil {
		return fmt.Errorf("write meta-data: %w", err)
	}
	if err := os.WriteFile(userPath, []byte(userData), 0o644); err != nil {
		return fmt.Errorf("write user-data: %w", err)
	}
	if err := os.WriteFile(netPath, []byte(networkConfig), 0o644); err != nil {
		return fmt.Errorf("write network-config: %w", err)
	}

	seedPath := filepath.Join(spec.dir, "seed.iso")
	cmd := exec.Command(
		"xorriso", "-as", "mkisofs",
		"-o", seedPath,
		"-volid", "cidata",
		"-joliet", "-rock",
		metaPath, userPath, netPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build seed iso (xorriso): %s: %w", string(out), err)
	}
	return nil
}

// vmNetworkXML is the libvirt network the guest's Star reconciler uses for
// the VMs it creates (VM_NETWORK/VM_IP_BASE env in node.env match it).
func vmNetworkXML(name, ipBase string) string {
	return fmt.Sprintf(`<network>
  <name>%s</name>
  <bridge name='virbr-c12s'/>
  <forward mode='nat'/>
  <ip address='%s.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='%s.2' end='%s.254'/>
    </dhcp>
  </ip>
</network>
`, name, ipBase, ipBase, ipBase)
}

// deterministicMAC maps a VM's static IPv4 to a stable MAC in the QEMU/KVM
// OUI (52:54:00), so the seed's network-config can match the NIC before the
// VM exists and virt-install can pre-assign the same MAC. Derived from the
// address (unique per VM) rather than a name hash, which collided.
func deterministicMAC(address string) string {
	ip := net.ParseIP(address).To4()
	if ip == nil {
		return "52:54:00:00:00:01"
	}
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", ip[1], ip[2], ip[3])
}

// nodeEnvLines is the per-node part of node.env. Shared settings and the
// control-plane addresses ship in the OS image (/usr/lib/c12s/node.env); the
// seed adds only what differs per machine, plus an optional NATS override.
func nodeEnvLines(natsAddress string, extras map[string]string) []string {
	lines := []string{"# per-node values; defaults: /usr/lib/c12s/node.env (image)"}
	if natsAddress != "" {
		lines = append(lines, "NATS_ADDRESS="+natsAddress)
	}
	keys := make([]string, 0, len(extras))
	for k := range extras {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, k+"="+extras[k])
	}
	return lines
}

const (
	defaultLoginUser     = "c12s"
	defaultLoginPassword = "c12s"
)

var loginUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// seedLogin is the resolved login user of a machine's seed.
type seedLogin struct {
	user     string
	password string // empty: password login disabled
	keys     []string
}

// resolveLogin applies the defaults to the config's login block and reads
// the key files, so a bad path fails before any VM is created.
func resolveLogin(conf *config.Login) (seedLogin, error) {
	login := seedLogin{user: defaultLoginUser, password: defaultLoginPassword}
	var keySources []string
	if conf != nil {
		if conf.User != "" {
			login.user = conf.User
		}
		if conf.Password != nil {
			login.password = *conf.Password
		}
		keySources = conf.SSHKeys
	}
	if !loginUserRe.MatchString(login.user) {
		return login, fmt.Errorf("login.user %q is not a valid user name", login.user)
	}
	if len(keySources) == 0 {
		login.keys = defaultSSHPubKeys()
	}
	for _, src := range keySources {
		key, err := readSSHKey(src)
		if err != nil {
			return login, err
		}
		login.keys = append(login.keys, key)
	}
	if login.password == "" && len(login.keys) == 0 {
		return login, fmt.Errorf("login: no password and no ssh keys, nobody could log in")
	}
	return login, nil
}

// readSSHKey takes a literal public key ("ssh-ed25519 AAAA...") or a path to
// a .pub file.
func readSSHKey(src string) (string, error) {
	if strings.HasPrefix(src, "ssh-") || strings.HasPrefix(src, "ecdsa-") || strings.HasPrefix(src, "sk-") {
		return strings.TrimSpace(src), nil
	}
	path := src
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("login.sshKeys: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("login.sshKeys: %s is empty", src)
	}
	return key, nil
}

// defaultSSHPubKeys reads ~/.ssh/id_*.pub, so the host user can ssh in.
func defaultSSHPubKeys() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_*.pub"))
	var keys []string
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		if key := strings.TrimSpace(string(data)); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

// renderUserData is the #cloud-config of a machine: its login user, then the
// files and commands the seed adds.
func renderUserData(login seedLogin, writeFiles, runcmd string) string {
	var b strings.Builder
	b.WriteString("#cloud-config\nusers:\n")
	fmt.Fprintf(&b, "  - name: %s\n    gecos: c12s login user\n    groups: [wheel]\n    shell: /bin/bash\n", login.user)
	fmt.Fprintf(&b, "    lock_passwd: %t\n    sudo: ALL=(ALL) NOPASSWD:ALL\n", login.password == "")
	if len(login.keys) > 0 {
		b.WriteString("    ssh_authorized_keys:\n")
		for _, key := range login.keys {
			fmt.Fprintf(&b, "      - %s\n", key)
		}
	}
	if login.password != "" {
		fmt.Fprintf(&b, "chpasswd:\n  expire: false\n  users:\n    - name: %s\n      password: %q\n      type: text\n", login.user, login.password)
	}
	fmt.Fprintf(&b, "ssh_pwauth: %t\n", login.password != "")
	if writeFiles != "" {
		b.WriteString("write_files:\n" + writeFiles)
		if !strings.HasSuffix(writeFiles, "\n") {
			b.WriteString("\n")
		}
	}
	if runcmd != "" {
		b.WriteString("runcmd:\n" + runcmd + "\n")
	}
	return b.String()
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
