package main

import (
	"fmt"
	"github.com/icook/ngpool/pkg/service"
	"github.com/spf13/cobra"
	"os"
	"os/signal"
	"syscall"
)

var RootCmd = &cobra.Command{
	Use:   "coinbuddy",
	Short: "A coinserver sidekick",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

func init() {
	ng := NewNgpool()
	getAttributes := func() map[string]interface{} {
		return map[string]interface{}{}
	}
	s := service.NewService("stratum", ng.config, getAttributes)
	s.SetupCmds(RootCmd)
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run the coinbuddy and coinserver",
		Run: func(cmd *cobra.Command, args []string) {
			defer ng.Stop()
			ng.Run()

			// Wait until we recieve sigint
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
			<-sigs
			// Defered cleanup is performed now
		}}

	RootCmd.AddCommand(runCmd)
}

func main() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
