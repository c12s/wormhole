package cmd

import (
	"fmt"
	"os"

	"github.com/c12s/wormhole/aliases"
	"github.com/c12s/wormhole/constants"
	"github.com/spf13/cobra"
)

var StopNodesCmd = &cobra.Command{
	Use:     "stop-nodes [VM names...]",
	Aliases: aliases.StartNodesAliases,
	Short:   constants.ShortStartNodesDesc,
	Long:    constants.LongStartNodesDesc,
	Run: func(cmd *cobra.Command, args []string) {
		backend, _ := backendFromConfig(configFile)

		err := backend.StartNodes(args...)
		if err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}

		fmt.Println("Nodes stopped successfully!")
	},
}
