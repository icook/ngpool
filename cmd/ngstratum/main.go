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
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ng := NewStratumServer()
			defer ng.Stop()
			ng.ConfigureService(args[0],
				[]string{"http://127.0.0.1:2379", "http://127.0.0.1:4001"})
			ng.ParseConfig()
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
