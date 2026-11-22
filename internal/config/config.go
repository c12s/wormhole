package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	VMCount      int    `yaml:"vmCount"`
	OSDistro     string `yaml:"osDistro"`
	OSVersion    string `yaml:"osVersion"`
	CPUs         int    `yaml:"cpus"`
	Memory       int    `yaml:"memory"`
	GUI          bool   `yaml:"gui"`
	IPBase       string `yaml:"ipBase"`
	GuestPort    int    `yaml:"guestPort"`
	HostPortBase int    `yaml:"hostPortBase"`
	NameBase     string `yaml:"nameBase"`
	BackendType  string `yaml:"backendType"`

	// libvirt backend
	DiskImage   string            `yaml:"diskImage"`   // source qcow2 the VMs are created from (required)
	Network     string            `yaml:"network"`     // libvirt network, default "default"
	OSVariant   string            `yaml:"osVariant"`   // virt-install --os-variant, default "fedora-unknown"
	StateDir    string            `yaml:"stateDir"`    // per-VM disks + seeds, default ~/.wormhole/libvirt
	NATSAddress string            `yaml:"natsAddress"` // optional override; the default ships in the image
	UEFI        bool              `yaml:"uefi"`        // boot via OVMF (bootc images are UEFI-only)
	CPUModel    string            `yaml:"cpuModel"`    // virt-install --cpu (e.g. host-passthrough for nested KVM)
	VMNetwork   string            `yaml:"vmNetwork"`   // guest-side libvirt network for VMs this node will host
	VMIpBase    string            `yaml:"vmIpBase"`    // subnet base of that network, e.g. "192.168.123"
	IPOffset    int               `yaml:"ipOffset"`    // <name>-N gets ipBase+ipOffset+N, default 50; keep ranges disjoint across configs
	Extras      map[string]string `yaml:"extras"`      // extra key=value pairs for node.env
	Login       *Login            `yaml:"login"`       // login user the seed creates; nil = c12s/c12s + ~/.ssh/id_*.pub
}

// Login is the user cloud-init creates on each machine. Unset fields keep
// the defaults: user c12s, password c12s, keys from ~/.ssh/id_*.pub.
type Login struct {
	User     string   `yaml:"user"`
	Password *string  `yaml:"password"` // "" = no password login (ssh keys only)
	SSHKeys  []string `yaml:"sshKeys"`  // public-key files (~ allowed) or literal keys
}

func (conf *Config) Validate() error {
	if conf.VMCount <= 0 {
		return fmt.Errorf("vmCount must be greater than 0")
	}
	if conf.CPUs <= 0 {
		return fmt.Errorf("cpus must be greater than 0")
	}
	if conf.Memory <= 0 {
		return fmt.Errorf("memory must be greater than 0")
	}
	if conf.NameBase == "" {
		return fmt.Errorf("nameBase cannot be empty")
	}
	switch conf.BackendType {
	case "libvirt":
		return conf.validateLibvirt()
	case "vagrant":
		return conf.validateVagrant()
	default:
		return fmt.Errorf("unsupported backend: %s", conf.BackendType)
	}
}

func (conf *Config) validateVagrant() error {
	if conf.OSDistro == "" {
		return fmt.Errorf("osDistro cannot be empty")
	}
	if conf.OSVersion == "" {
		return fmt.Errorf("osVersion cannot be empty")
	}
	if conf.IPBase == "" {
		return fmt.Errorf("ipBase cannot be empty")
	}
	if conf.GuestPort < 1024 || conf.GuestPort > 65535 {
		return fmt.Errorf("guestPort must be between 1024 and 65535")
	}
	if conf.HostPortBase < 1024 || conf.HostPortBase > 65535 {
		return fmt.Errorf("hostPortBase must be between 1024 and 65535")
	}
	return nil
}

func (conf *Config) validateLibvirt() error {
	if conf.DiskImage == "" {
		return fmt.Errorf("diskImage is required for the libvirt backend")
	}
	if conf.IPBase == "" {
		return fmt.Errorf("ipBase is required for the libvirt backend (static per-VM addressing)")
	}
	if conf.VMNetwork != "" && conf.VMIpBase == "" {
		return fmt.Errorf("vmIpBase is required when vmNetwork is set (subnet for guest-hosted VMs)")
	}
	return nil
}

func expandTilde(path *string) {
	if path == nil || !strings.HasPrefix(*path, "~/") {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	*path = filepath.Join(home, (*path)[2:])
}

func LoadFromYaml(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	var conf Config
	err = yaml.Unmarshal(data, &conf)
	if err != nil {
		return nil, fmt.Errorf("unmarshal yaml: %w", err)
	}
	expandTilde(&conf.DiskImage)
	expandTilde(&conf.StateDir)

	err = conf.Validate()
	if err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &conf, nil
}
