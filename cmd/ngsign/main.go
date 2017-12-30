package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	log "github.com/inconshreveable/log15"
	"github.com/levigross/grequests"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/icook/ngpool/pkg/common"
	"github.com/icook/ngpool/pkg/service"
)

func sign(config *service.ChainConfig, urlbase string,
	addresses map[string]*btcec.PrivateKey) error {
	resp, err := grequests.Get(urlbase+"/v1/createpayout/"+config.Code, nil)
	if err != nil {
		return err
	}
	type Payout struct {
		Errors []interface{}
		Data   struct {
			PayoutMaps map[string]common.PayoutMap `json:"payout_maps"`
			TX         string
			Inputs     []common.UTXO
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
	var payout = vals.Data
	log.Info("Got pending payout",
		"outputs", len(payout.PayoutMaps),
		"tx_size", len(payout.TX))

	redeemTx := wire.NewMsgTx(0)
	dec, err := hex.DecodeString(payout.TX)
	if err != nil {
		return err
	}
	redeemTx.Deserialize(bytes.NewReader(dec))

	lookupKey := func(a btcutil.Address) (*btcec.PrivateKey, bool, error) {
		addr, ok := addresses[a.EncodeAddress()]
		return addr, ok, nil
	}

	for i, input := range redeemTx.TxIn {
		var inputAddress string
		cmp := input.PreviousOutPoint.Hash.String()
		for _, inputUTXO := range payout.Inputs {
			if inputUTXO.Hash == cmp && input.PreviousOutPoint.Index == inputUTXO.Vout {
				inputAddress = inputUTXO.Address
				break
			}
		}
		if inputAddress == "" {
			log.Error("Failed linking input to UTXO", "i", i, "prevout", input.PreviousOutPoint)
			return errors.New("Failed to find UTXO linking to input")
		}

		addr, err := btcutil.DecodeAddress(inputAddress, config.Params)
		if err != nil {
			return errors.New("Invalid address in UTXO from server")
		}

		pkScript, err := txscript.PayToAddrScript(addr)
		if err != nil {
			return err
		}

		sigScript, err := txscript.SignTxOutput(config.Params,
			redeemTx, i, pkScript, txscript.SigHashAll,
			txscript.KeyClosure(lookupKey), nil, nil)

		redeemTx.TxIn[i].SignatureScript = sigScript
	}

	// Push back to server
	out := bytes.Buffer{}
	redeemTx.Serialize(&out)
	log.Info("Signed tx", "tx", hex.EncodeToString(out.Bytes()))
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

func loadAddresses(addrs []string, params *chaincfg.Params) (map[string]*btcec.PrivateKey, error) {
	var ret = map[string]*btcec.PrivateKey{}
	for idx, privkey := range addrs {
		wif, err := btcutil.DecodeWIF(privkey)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid idx %v", idx)
		}
		addr, err := btcutil.NewAddressPubKey(wif.PrivKey.PubKey().SerializeCompressed(), params)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid idx %v", idx)
		}
		ret[addr.EncodeAddress()] = wif.PrivKey
	}
	return ret, nil
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
			logger := log.New("currency", curr.Code)
			subcfg := config.Sub(curr.Code)
			if subcfg == nil {
				logger.Info("Skipping unconfigured")
				continue
			}
			addresses, err := loadAddresses(subcfg.GetStringSlice("keys"), curr.Params)
			if err != nil {
				logger.Crit("Invalid address", "err", err)
				os.Exit(1)
			}

			err = sign(curr, args[0], addresses)
			if err != nil {
				logger.Crit("Failed signing", "err", err)
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
