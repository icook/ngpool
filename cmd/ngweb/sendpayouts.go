package main

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"time"

	"github.com/icook/ngpool/pkg/common"
)

func init() {
	sendPayoutsCmd := &cobra.Command{
		Use: "sendpayouts",
		Run: func(cmd *cobra.Command, args []string) {
			ng := NewNgWebAPI()
			ng.ParseConfig()
			ng.ConnectDB()
			ng.WatchCoinservers()
			err := ng.sendPayoutTransactions()
			if err != nil {
				ng.log.Crit("Failed", "err", err)
			}
		},
	}
	RootCmd.AddCommand(sendPayoutsCmd)
}

func (q *NgWebAPI) sendPayoutTransactions() error {
	type PayoutTransaction struct {
		SignedTX string `db:"signed_tx"`
		Currency string
		Hash     string
		Sent     *time.Time
	}
	var txs []PayoutTransaction
	err := q.db.Select(&txs,
		`SELECT encode(signed_tx, 'hex') as signed_tx, hash, currency, sent FROM payout_transaction
		WHERE confirmed = false`)
	if err != nil {
		return err
	}

	for _, tx := range txs {
		rpc, ok := q.getRPC(tx.Currency)
		if !ok {
			q.log.Warn("Failed to grab RPC server, skipping",
				"hash", tx.Hash, "currency", tx.Currency)
			continue
		}

		txObj, err := common.HexStringToTX(tx.SignedTX)
		if !ok {
			q.log.Error("Failed to deser signed_tx", "hash", tx.Hash, "err", err)
			continue
		}

		resp, err := rpc.SendRawTransaction(txObj, false)
		if err != nil {
			q.log.Error("Failed sending pushed raw tx", "err", err, "tx", tx.SignedTX)
			continue
		}
		// Double check here for safety
		if resp.String() != tx.Hash {
			q.log.Crit("Payout transaction mismatch, database is inconsistent!",
				"out_tx_hash", tx.Hash, "rpc_tx_hash", resp.String())
			return errors.New("Invalid state") // Abort so operator has to fix problem
		}

		_, err = q.db.Exec(
			`UPDATE payout_transaction SET sent = now() WHERE hash = $1`, tx.Hash)
		if err != nil {
			q.log.Error("Error sending transaction")
		}
	}
	return nil
}
