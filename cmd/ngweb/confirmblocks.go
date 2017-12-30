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
		endpoint := service.Labels["endpoint"]
		currency := service.Labels["currency"]
		connCfg := &rpcclient.ConnConfig{
			Host:         endpoint[7:] + "rpc",
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
		// For setting the utxo spendable
		CoinbaseHash string `db:"coinbase_hash"`
	}
	var blocks []HashCurrency
	err = q.db.Select(&blocks,
		`SELECT hash, currency, height, coinbase_hash FROM block WHERE status = 'immature'`)
	if err != nil {
		return err
	}
	currencyHeights := map[string]int64{}
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

		_, ok = currencyHeights[block.Currency]
		if !ok {
			count, err := rpc.GetBlockCount()
			if err != nil {
				q.log.Error("Failed to get block count from rpc", "block", block, "rpc", rpc, "err", err)
				continue
			}
			currencyHeights[block.Currency] = count
		}
		height := currencyHeights[block.Currency]

		var newStatus string
		if resp.Confirmations >= config.BlockMatureConfirms {
			newStatus = "mature"
			q.log.Info("Marked block confirmed",
				"block", block,
				"confirms", resp.Confirmations,
				"reqconfirms", config.BlockMatureConfirms)
		} else if resp.Confirmations == -1 && (height-resp.Height) > config.BlockMatureConfirms {
			newStatus = "orphan"
			q.log.Warn("Block orphan",
				"block", block,
				"chainHeight", height,
				"blockHeight", resp.Height,
				"reqorphanconfirms", config.BlockMatureConfirms)
		} else {
			q.log.Debug("Block not mature",
				"block", block,
				"confirms", resp.Confirmations,
				"remain", config.BlockMatureConfirms-resp.Confirmations,
				"reqconfirms", config.BlockMatureConfirms)
		}

		if newStatus != "" {
			tx, err := q.db.Begin()
			_, err = tx.Exec(
				`UPDATE block SET status = $1 WHERE hash = $2`, newStatus, block.Hash)
			if err != nil {
				tx.Rollback()
				q.log.Error("Failed to update block status",
					"block", block, "status", newStatus, "err", err)
				continue
			}

			if newStatus == "mature" {
				_, err := tx.Exec(
					`UPDATE utxo SET spendable = true WHERE hash = $1`, block.CoinbaseHash)
				if err != nil {
					tx.Rollback()
					q.log.Error("Failed to update utxo spendable",
						"block", block, "err", err)
					continue
				}
			}
			err = tx.Commit()
			if err != nil {
				q.log.Error("Failed to commit block/utxo status",
					"block", block, "status", newStatus, "err", err)
				continue
			}
		}
	}

	q.log.Info("Running generate credits to process newly matured blocks")
	err = q.GenerateCredits()
	if err != nil {
		q.log.Error("GenerateCredits failed", "err", err)
	}
	return nil
}
