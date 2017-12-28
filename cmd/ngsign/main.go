package main

import (
	"fmt"
	"github.com/btcsuite/btcutil"
	"github.com/icook/ngpool/pkg/service"
	log "github.com/inconshreveable/log15"
	"github.com/levigross/grequests"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"os"
)

func sign(config *service.ChainConfig, urlbase string) error {
	resp, err := grequests.Get(urlbase+"/v1/createpayout/"+config.Code, nil)
	if err != nil {
		return err
	}
	type Payout struct {
		Errors []interface{}
		Data   struct {
			PayoutMaps []struct {
				CreditIDs []int
				UserID    int
				Address   string
				Amount    int64
				MinerFee  int64

				AddressObj btcutil.Address
			}
			TX string
		}
	}
	var vals Payout
	err = resp.JSON(&vals)
	if err != nil {
		return err
	}
	if len(vals.Errors) > 0 {
		log.Error("Error from server", "errors", vals.Errors)
		return errors.New("Error from remote")
	}
	log.Info("Got raw payout", "data", vals.Data)
	return nil
}

// Loads currency config from the remote
func loadCommon(urlbase string) {
	resp, err := grequests.Get(urlbase+"/v1/common", nil)
	if err != nil {
		log.Crit("Failed to get common config", "urlbase", urlbase)
		os.Exit(1)
	}
	type Resp struct {
		Data struct {
			Currencies map[string]interface{} `json:"raw_currencies"`
		}
	}
	var vals Resp
	resp.JSON(&vals)
	service.SetupCurrencies(vals.Data.Currencies)
}

var RootCmd = &cobra.Command{
	Use:   "ngsign [urlbase] [keyfile]",
	Short: "Sign raw transactions",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		urlbase := args[0]
		keyfile := args[1]

		config := viper.New()
		config.AddConfigPath(".")
		config.AddConfigPath("../../")
		config.SetConfigType("yaml")
		config.SetConfigName(keyfile)
		err := config.ReadInConfig()
		if err != nil {
			log.Crit("error parsing configuration file", "err", err)
			os.Exit(1)
		}

		loadCommon(urlbase)
		for _, curr := range service.CurrencyConfig {
			err = sign(curr, args[0])
			if err != nil {
				log.Crit("Failed signing", "err", err)
			}
		}
	},
}

func main() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
