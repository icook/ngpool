package main

import (
	"fmt"
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
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run the coinbuddy and coinserver",
		Run: func(cmd *cobra.Command, args []string) {
			ng := NewNgWebAPI()
			ng.ParseConfig()
			ng.SetupGin()
			ng.engine.Run()

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
