package main

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
	"io/ioutil"
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
	var (
		fileName string
	)
	loadconfigCmd := &cobra.Command{
		Use:   "pushconfig",
		Short: "Loads the config and displays it",
		Run: func(cmd *cobra.Command, args []string) {
			fileInput, err := ioutil.ReadFile(fileName)
			cb := NewCoinBuddy("config")
			serviceID := cb.config.GetString("ServiceID")
			if serviceID == "" {
				log.Fatal("Cannot push config to etcd without a ServiceID (hint: export SERVICEID=veryuniquestring")
			}
			_, err = cb.etcdKeys.Set(
				context.Background(), "/config/"+serviceID, string(fileInput), nil)
			if err != nil {
				log.WithError(err).Fatal("Failed pushing config")
			}
			log.Infof("Successfully pushed '%s' to /config/%s", fileName, serviceID)
		}}
	loadconfigCmd.Flags().StringVarP(&fileName, "config", "c", "", "the config to load")
	dumpconfigCmd := &cobra.Command{
		Use:   "dumpconfig",
		Short: "Loads the config and displays it",
		Run: func(cmd *cobra.Command, args []string) {
			cb := NewCoinBuddy("config")
			b, err := yaml.Marshal(cb.config.AllSettings())
			if err != nil {
				fmt.Println("error:", err)
			}
			fmt.Println(string(b))
		}}
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run the coinbuddy and coinserver",
		Run: func(cmd *cobra.Command, args []string) {
			cb := NewCoinBuddy("config")
			defer cb.Stop()
			cb.RunEtcdHealth()
			err := cb.RunCoinserver()
			if err != nil {
				log.WithError(err).Fatal("Coinserver never came up for 90 seconds")
			}
			cb.RunBlockListener()
			cb.RunEventListener()

			// Wait until we recieve sigint
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
			<-sigs
			// Defered cleanup is performed now
		}}

	RootCmd.AddCommand(dumpconfigCmd)
	RootCmd.AddCommand(loadconfigCmd)
	RootCmd.AddCommand(runCmd)
}

func main() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
