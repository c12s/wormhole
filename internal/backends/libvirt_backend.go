package backends

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/c12s/wormhole/internal/config"
)

// LibvirtBackend provisions bootc node VMs on the local KVM host:
//
//   each VM is a qcow2 overlay over a Supernova-produced bootc OS image,
//   plus a NoCloud seed that injects /var/lib/c12s/node.env on first boot.
//   star runs inside the VM as a systemd service and enrolls into the CP
//   like any other node — provisioning stays out of band of the control
//   plane.

const (
	libvirtURI        = "qemu:///system"
	libvirtNamePrefix = "c12s-"
	libvirtDiskSizeGB = 20
)

type LibvirtBackend struct {
	conf *config.Config
}

func NewLibvirtBackend(conf *config.Config) (*LibvirtBackend, error) {
	return &LibvirtBackend{conf: conf}, nil
}

func (l *LibvirtBackend) Setup(conf *config.Config) error {
	l.conf = conf
	if _, err := os.Stat(conf.DiskImage); err != nil {
		return fmt.Errorf("source disk image: %w", err)
	}
	for _, bin := range []string{"virt-install", "virsh", "xorriso"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not found in PATH", bin)
		}
	}
	if err := os.MkdirAll(l.stateDir(), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	return nil
}

func (l *LibvirtBackend) stateDir() string {
	if l.conf.StateDir != "" {
		return expandHome(l.conf.StateDir)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wormhole", "libvirt")
}

func (l *LibvirtBackend) domain(name string) string {
	return libvirtNamePrefix + name
}

func (l *LibvirtBackend) vmDir(name string) string {
	return filepath.Join(l.stateDir(), name)
}

// names defaults an empty selection to every configured VM
// (<nameBase>-1 .. <nameBase>-<vmCount>), matching vagrant's "all" semantics.
func (l *LibvirtBackend) names(vms []string) []string {
	if len(vms) > 0 {
		return vms
	}
	all := make([]string, 0, l.conf.VMCount)
	for i := 1; i <= l.conf.VMCount; i++ {
		all = append(all, fmt.Sprintf("%s-%d", l.conf.NameBase, i))
	}
	return all
}

func (l *LibvirtBackend) Create(vms ...string) error {
	if err := l.Setup(l.conf); err != nil {
		return err
	}
	// resolve the login once: a bad key path fails before any VM exists
	login, err := resolveLogin(l.conf.Login)
	if err != nil {
		return err
	}
	for _, vm := range l.names(vms) {
		if l.domainExists(l.domain(vm)) {
			fmt.Printf("==> %s already exists, skipping (destroy it to recreate)\n", l.domain(vm))
			continue
		}
		if err := l.createVM(vm, login); err != nil {
			return fmt.Errorf("create %s: %w", vm, err)
		}
	}
	return nil
}

func (l *LibvirtBackend) createVM(name string, login seedLogin) error {
	dir := l.vmDir(name)
	disk := filepath.Join(dir, "disk.qcow2")
	seed := filepath.Join(dir, "seed.iso")
	domain := l.domain(name)
	address, err := staticAddress(name, l.conf.IPBase, l.ipOffset())
	if err != nil {
		return err
	}
	mac := deterministicMAC(address)
	if owner := l.addressOwner(address, name); owner != "" {
		return fmt.Errorf("address %s already used by wormhole VM %s (set a disjoint ipOffset in the config)", address, owner)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if err := buildSeed(seedSpec{
		dir:         dir,
		domain:      domain,
		mac:         mac,
		address:     address,
		gateway:     gatewayFromBase(l.conf.IPBase),
		natsAddress: l.conf.NATSAddress,
		extras:      l.seedExtras(),
		vmNetwork:   l.conf.VMNetwork,
		vmIpBase:    l.conf.VMIpBase,
		login:       login,
	}); err != nil {
		return err
	}
	// per-VM copy of the source image: libvirt's dynamic ownership chowns
	// every file in a disk's backing chain, so the pristine source must not
	// be the backing file directly (a reflink copy is instant on CoW fs).
	base := filepath.Join(dir, "base.qcow2")
	if err := copyReflink(l.conf.DiskImage, base); err != nil {
		return err
	}
	if err := createDiskOverlay(base, disk); err != nil {
		return err
	}

	fmt.Printf("==> creating VM %s (domain %s, mac %s, ip %s)\n", name, domain, mac, address)
	args := []string{
		"--name", domain,
		"--memory", fmt.Sprintf("%d", l.conf.Memory),
		"--vcpus", fmt.Sprintf("%d", l.conf.CPUs),
		"--disk", "path=" + disk + ",format=qcow2",
		"--disk", "path=" + seed + ",device=cdrom",
		"--network", "network=" + l.networkName() + ",model=virtio,mac=" + mac,
		"--os-variant", l.osVariant(),
		graphicsFlag(l.conf.GUI),
	}
	if l.conf.UEFI {
		args = append(args, "--boot", "uefi")
	}
	if l.conf.CPUModel != "" {
		// e.g. host-passthrough so a guest that will host VMs sees the
		// virtualization extensions (nested KVM)
		args = append(args, "--cpu", l.conf.CPUModel)
	}
	return l.virshInstall(args...)
}

func (l *LibvirtBackend) Start(vms ...string) error {
	return l.forEach(vms, "start", nil)
}

// Resume is the same as Start for libvirt: a stopped VM boots from disk.
func (l *LibvirtBackend) Resume(vms ...string) error {
	return l.forEach(vms, "start", nil)
}

// Reload reboots a running VM (ACPI request to the guest).
func (l *LibvirtBackend) Reload(vms ...string) error {
	return l.forEach(vms, "reboot", nil)
}

// Stop = graceful ACPI shutdown, waits for the guest.
func (l *LibvirtBackend) Stop(vms ...string) error {
	return l.forEach(vms, "shutdown", nil)
}

// ShutDown = hard poweroff.
func (l *LibvirtBackend) ShutDown(vms ...string) error {
	return l.forEach(vms, "destroy", nil)
}

// StartNodes: on a bootc VM the node agent *is* the boot (star.service),
// so starting the node = starting the VM.
func (l *LibvirtBackend) StartNodes(vms ...string) error {
	return l.forEach(vms, "start", nil)
}

func (l *LibvirtBackend) StopNodes(vms ...string) error {
	return l.forEach(vms, "shutdown", nil)
}

func (l *LibvirtBackend) Destroy(vms ...string) error {
	for _, vm := range l.names(vms) {
		domain := l.domain(vm)
		fmt.Printf("==> destroying %s\n", domain)
		// destroy (poweroff) first so undefine always works
		_ = l.virsh("destroy", domain)
		if err := l.virsh("undefine", domain, "--nvram", "--remove-all-storage"); err != nil {
			// remove-all-storage would delete the overlay disk; seed stays
			// in the state dir and is cleaned below anyway
			if err2 := l.virsh("undefine", domain, "--nvram"); err2 != nil {
				return fmt.Errorf("undefine %s: %w", domain, err)
			}
		}
		if err := os.RemoveAll(l.vmDir(vm)); err != nil {
			return fmt.Errorf("remove state for %s: %w", vm, err)
		}
	}
	return nil
}

func (l *LibvirtBackend) forEach(vms []string, action string, _ any) error {
	for _, vm := range l.names(vms) {
		domain := l.domain(vm)
		fmt.Printf("==> virsh %s %s\n", action, domain)
		if err := l.virsh(action, domain); err != nil {
			return fmt.Errorf("%s %s: %w", action, domain, err)
		}
	}
	return nil
}

func (l *LibvirtBackend) virshInstall(args ...string) error {
	full := append([]string{"--connect", libvirtURI, "--import", "--noautoconsole"}, args...)
	cmd := exec.Command("virt-install", full...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (l *LibvirtBackend) virsh(args ...string) error {
	cmd := exec.Command("virsh", append([]string{"-c", libvirtURI}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (l *LibvirtBackend) domainExists(domain string) bool {
	return l.virsh("domid", domain) == nil
}

func (l *LibvirtBackend) networkName() string {
	if l.conf.Network != "" {
		return l.conf.Network
	}
	return "default"
}

// seedExtras is what lands in /var/lib/c12s/node.env on first boot: operator
// extras plus the reconciler settings a VM-hosting node needs.
func (l *LibvirtBackend) seedExtras() map[string]string {
	extras := map[string]string{}
	for k, v := range l.conf.Extras {
		extras[k] = v
	}
	if l.conf.VMNetwork != "" {
		extras["VM_NETWORK"] = l.conf.VMNetwork
	}
	if l.conf.VMIpBase != "" {
		extras["VM_IP_BASE"] = l.conf.VMIpBase
	}
	return extras
}

func (l *LibvirtBackend) osVariant() string {
	if l.conf.OSVariant != "" {
		return l.conf.OSVariant
	}
	return "fedora-unknown"
}

func (l *LibvirtBackend) ipOffset() int {
	if l.conf.IPOffset > 0 {
		return l.conf.IPOffset
	}
	return 50
}

// addressOwner returns the other VM in the state dir whose seed already
// claims address, or "" if it is free. Configs share the state dir and the
// subnet, so the first VM of two configs would otherwise get the same address.
func (l *LibvirtBackend) addressOwner(address, self string) string {
	seeds, _ := filepath.Glob(filepath.Join(l.stateDir(), "*", "network-config"))
	for _, seed := range seeds {
		vm := filepath.Base(filepath.Dir(seed))
		if vm == self {
			continue
		}
		data, err := os.ReadFile(seed)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "address: "+address {
				return vm
			}
		}
	}
	return ""
}

// staticAddress derives a stable IPv4 from the VM name: the trailing number
// (node-3 -> 3) is added to ipBase+offset, so a VM keeps its address across
// re-creation.
func staticAddress(name, ipBase string, offset int) (string, error) {
	digits := ""
	for _, r := range name {
		if r >= '0' && r <= '9' {
			digits += string(r)
		}
	}
	if digits == "" {
		return "", fmt.Errorf("vm name %q has no trailing number for static addressing (ipBase %s)", name, ipBase)
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 0 || offset+n > 254 {
		return "", fmt.Errorf("vm name %q: bad node number %q for ipOffset %d", name, digits, offset)
	}
	return ipBase + strconv.Itoa(offset+n), nil
}

func gatewayFromBase(ipBase string) string {
	return ipBase + "1"
}

// copyReflink copies a file using CoW reflink when the filesystem supports
// it (instant even for multi-GB images), falling back to a plain copy.
func copyReflink(src, dst string) error {
	if _, err := exec.LookPath("cp"); err == nil {
		if err := exec.Command("cp", "--reflink=auto", src, dst).Run(); err == nil {
			return nil
		}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// createDiskOverlay makes a small qcow2 overlay backed by the source image;
// bootc switch/rollback writes land in the overlay, the source image stays
// pristine (same trick as container image layers).
func createDiskOverlay(source, target string) error {
	absSource, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("qemu-img"); err == nil {
		cmd := exec.Command("qemu-img", "create", "-f", "qcow2", "-F", "qcow2",
			"-b", absSource, target, fmt.Sprintf("%dG", libvirtDiskSizeGB))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("qemu-img create: %s: %w", string(out), err)
		}
		return nil
	}
	// fallback: plain copy
	data, err := os.ReadFile(absSource)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

func graphicsFlag(gui bool) string {
	if gui {
		return "--graphics=vnc"
	}
	return "--graphics=none"
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}
