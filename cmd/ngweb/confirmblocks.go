package main

import (
	"encoding/hex"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/icook/btcd/rpcclient"
	"github.com/icook/ngpool/pkg/service"
	"github.com/spf13/cobra"
)

func init() {
	confirmBlocks := &cobra.Command{
		Use: "confirmblocks",
		Run: func(cmd *cobra.Command, args []string) {
			ng := NewNgWebAPI()
			ng.ParseConfig()
			ng.ConnectDB()
			err := ng.ConfirmBlocks()
			if err != nil {
				ng.log.Crit("Failed", "err", err)
			}
		},
	}
	RootCmd.AddCommand(confirmBlocks)
}

func (q *NgWebAPI) ConfirmBlocks() error {
	services, err := q.service.LoadServices("coinserver")
	if err != nil {
		return err
	}
	// group them by currency
	currencyCoinservers := map[string]*rpcclient.Client{}
	for _, service := range services {
		// TODO: Fail gracefully
		endpoint := service.Labels["endpoint"].(string)
		currency := service.Labels["currency"].(string)
		connCfg := &rpcclient.ConnConfig{
			Host:         endpoint[7:] + "rpc",
			User:         "",
			Pass:         "",
			HTTPPostMode: true, // Bitcoin core only supports HTTP POST mode
			DisableTLS:   true, // Bitcoin core does not provide TLS by default
		}
		client, err := rpcclient.New(connCfg, nil)
		if err != nil {
			return err
		}
		currencyCoinservers[currency] = client
	}

	type HashCurrency struct {
		Height   int64 // Only for logging/debugging
		Hash     string
		Currency string
	}
	var blocks []HashCurrency
	err = q.db.Select(&blocks, `SELECT hash, currency, height FROM block WHERE mature = false`)
	if err != nil {
		return err
	}
	for _, block := range blocks {
		config, ok := service.CurrencyConfig[block.Currency]
		if !ok {
			q.log.Error("Couldn't locate currency config", "block", block, "err", err)
			continue
		}
		rpc, ok := currencyCoinservers[block.Currency]
		if !ok {
			q.log.Warn("Skipping block, no coinserver live", "block", block, "err", err)
			continue
		}
		decHash, err := hex.DecodeString(block.Hash)
		if err != nil {
			q.log.Error("Invalid block hash in db", "block", block, "err", err)
			continue
		}
		hashObj, err := chainhash.NewHash(decHash)
		if err != nil {
			q.log.Error("Invalid block hash in db", "block", block, "err", err)
			continue
		}
		resp, err := rpc.GetBlockVerbose(hashObj)
		if err != nil {
			q.log.Error("Failed to get block information from rpc", "block", block, "err", err)
			continue
		}
		if resp.Confirmations >= config.BlockMatureConfirms {
			_, err := q.db.Exec(
				`UPDATE block SET mature = true WHERE hash = $1`, block.Hash)
			if err != nil {
				q.log.Error("Failed to update block mature status", "block", block, "err", err)
				continue
			}
			q.log.Info("Marked block confirmed",
				"block", block,
				"confirms", resp.Confirmations,
				"reqconfirms", config.BlockMatureConfirms)
		} else {
			q.log.Info("Block not mature",
				"block", block,
				"confirms", resp.Confirmations,
				"remain", config.BlockMatureConfirms-resp.Confirmations,
				"reqconfirms", config.BlockMatureConfirms)
		}

	}
	return nil
}
