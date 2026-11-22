package core

import (
	"fmt"

	"github.com/c12s/wormhole/internal/backends"
	"github.com/c12s/wormhole/internal/config"
)

type Backend interface {
	Setup(conf *config.Config) error
	Create(vms ...string) error
	Stop(vms ...string) error
	Resume(vms ...string) error
	Reload(vms ...string) error
	ShutDown(vms ...string) error
	Destroy(vms ...string) error
	StartNodes(vms ...string) error
	StopNodes(vms ...string) error
}

func NewBackend(name string) (Backend, error) {
	switch name {
	case "vagrant":
		return backends.NewVagrantBackend()
	default:
		return nil, fmt.Errorf("unsupported backend: %s", name)
	}
}

// NewBackendForConfig constructs the backend selected by config.yaml; the
// libvirt backend needs the config itself (disk image, network, node env).
func NewBackendForConfig(conf *config.Config) (Backend, error) {
	switch conf.BackendType {
	case "vagrant":
		return NewBackend("vagrant")
	case "libvirt":
		return backends.NewLibvirtBackend(conf)
	default:
		return nil, fmt.Errorf("unsupported backend: %s", conf.BackendType)
	}
}
