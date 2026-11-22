package cmd

import (
	"fmt"
	"os"

	"github.com/c12s/wormhole/aliases"
	"github.com/c12s/wormhole/constants"
	"github.com/spf13/cobra"
)

var StopCmd = &cobra.Command{
	Use:     "stop [VM names...]",
	Aliases: aliases.StopAliases,
	Short:   constants.ShortStopDesc,
	Long:    constants.LongStopDesc,
	Run: func(cmd *cobra.Command, args []string) {
		backend, _ := backendFromConfig(configFile)

		err := backend.Stop(args...)
		if err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}

		fmt.Println("VMs stopped successfully!")
	},
}
