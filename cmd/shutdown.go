package cmd

import (
	"fmt"
	"os"

	"github.com/c12s/wormhole/aliases"
	"github.com/c12s/wormhole/constants"
	"github.com/spf13/cobra"
)

var ShutDownCmd = &cobra.Command{
	Use:     "shutdown [VM names...]",
	Aliases: aliases.ReloadAliases,
	Short:   constants.ShortShutDownDesc,
	Long:    constants.LongShutDownDesc,
	Run: func(cmd *cobra.Command, args []string) {
		backend, _ := backendFromConfig(configFile)

		err := backend.ShutDown(args...)
		if err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}

		fmt.Println("VMs shut down successfully!")
	},
}
