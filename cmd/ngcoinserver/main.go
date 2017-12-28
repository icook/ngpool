package main

import (
	"fmt"
	log "github.com/inconshreveable/log15"
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
	var net string
	genkeyCmd := &cobra.Command{
		Use:   "genkey [binary]",
		Short: "Generate a keypair for the using the given binary",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			datadir := "./tmpcoinserver"
			config := map[string]string{
				"datadir":     datadir,
				"connect":     "0",
				"rpcport":     "21000",
				"rpcuser":     "admin1",
				"rpcpassword": "123",
			}
			switch net {
			case "test":
				config["testnet"] = "1"
			case "regtest":
				config["regtest"] = "1"
			}

			cs := NewCoinserver(config, "", args[0])
			defer func() {
				cs.Stop()
				os.RemoveAll(datadir)
			}()

			err := cs.Run()
			if err != nil {
				log.Crit("Failed to start coinserver", "err", err)
				os.Exit(1)
			}
			cs.WaitUntilUp()
			pub, priv, err := cs.GenerateKeypair()
			if err != nil {
				log.Crit("Failed to generate", "err", err)
				os.Exit(1)
			}
			fmt.Println("Pubkey: ", pub)
			fmt.Println("Privkey: ", priv)
		}}

	genkeyCmd.Flags().StringVarP(&net, "net", "n", "main", "main,regtest,test")
	RootCmd.AddCommand(genkeyCmd)

	runCmd := &cobra.Command{
		Use:   "run [name]",
		Short: "Run the coinbuddy and coinserver",
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) > 0 {
				os.Setenv("SERVICEID", args[0])
			}
			cb := NewCoinBuddy()
			defer cb.Stop()
			cb.ConfigureService(args[0],
				[]string{"http://127.0.0.1:2379", "http://127.0.0.1:4001"})
			cb.ParseConfig()
			cb.Run()

			// Wait until we recieve sigint
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
			<-sigs
			// Defered cleanup is performed now
		},
	}

	RootCmd.AddCommand(runCmd)
}

func main() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
