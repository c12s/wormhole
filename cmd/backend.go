package cmd

import (
	"fmt"
	"os"

	"github.com/c12s/wormhole/internal/config"
	"github.com/c12s/wormhole/internal/core"
)

// backendFromConfig loads the wormhole config (explicit flag or ./config.yaml)
// and constructs the backend it selects. All lifecycle commands go through
// this so the backend is config-driven, not hardcoded.
func backendFromConfig(path string) (core.Backend, *config.Config) {
	conf, err := config.LoadFromYaml(path)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	backend, err := core.NewBackendForConfig(conf)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	return backend, conf
}

var configFile string

func init() {
	RootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "config.yaml", "Path to wormhole configuration YAML")
}
