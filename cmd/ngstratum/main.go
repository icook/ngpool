package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"os/signal"
	"syscall"
)

var RootCmd = &cobra.Command{
	Use:   "ngstratum",
	Short: "A stratum mining server",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

func init() {
	runCmd := &cobra.Command{
		Use:   "run [name]",
		Short: "Run the coinbuddy and coinserver",
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) > 0 {
				os.Setenv("SERVICEID", args[0])
			}
			ng := NewStratumServer()
			defer ng.Stop()
			ng.Start()

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
