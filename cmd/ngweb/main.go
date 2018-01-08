package main

import (
	"fmt"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"
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
	RootCmd.AddCommand(&cobra.Command{
		Use:   "run",
		Short: "Run the coinbuddy and coinserver",
		Run: func(cmd *cobra.Command, args []string) {
			ng := NewNgWebAPI()
			ng.ParseConfig()
			ng.ConnectDB()
			ng.SetupGin()
			ng.WatchCoinservers()
			ng.WatchStratum()
			ng.engine.Run()

			// Wait until we recieve sigint
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
			<-sigs
			// Defered cleanup is performed now
		}})

	RootCmd.AddCommand(&cobra.Command{
		Use:   "setpassword [username]",
		Short: "Set password for a user",
		Run: func(cmd *cobra.Command, args []string) {
			ng := NewNgWebAPI()
			ng.ParseConfig()
			ng.ConnectDB()

			prompt := promptui.Prompt{
				Label: "new password",
				Mask:  '*',
			}

			result, err := prompt.Run()
			if err != nil {
				panic(err)
			}

			bcryptPassword, err := bcrypt.GenerateFromPassword([]byte(result), 6)
			if err != nil {
				panic(err)
			}
			res, err := ng.db.Exec(
				`UPDATE users SET password = $1 WHERE username = $2`, bcryptPassword, args[0])
			affect, err := res.RowsAffected()
			if err == nil && affect == 0 {
				fmt.Println("Error: No such user")
			}
			if err != nil {
				panic(err)
			}
		},
	})
}

func main() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
