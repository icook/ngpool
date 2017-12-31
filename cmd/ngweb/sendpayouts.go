package main

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"

	"github.com/icook/ngpool/pkg/common"
)

func init() {
	updatePayoutsCmd := &cobra.Command{
		Use: "updatepayouttx",
		Run: func(cmd *cobra.Command, args []string) {
			ng := NewNgWebAPI()
			ng.ParseConfig()
			ng.ConnectDB()
			ng.WatchCoinservers()
			err := ng.updatePayoutTransactions()
			if err != nil {
				ng.log.Crit("Failed", "err", err)
			}
		},
	}
	RootCmd.AddCommand(updatePayoutsCmd)
}

func (q *NgWebAPI) updatePayoutTransactions() error {
	type PayoutTransaction struct {
		SignedTX string `db:"signed_tx"`
		Currency string
		Hash     string
		Vout     int
		Sent     *time.Time
	}
	var txs []PayoutTransaction
	err := q.db.Select(&txs,
		`SELECT encode(pt.signed_tx, 'hex') as signed_tx, pt.hash, pt.currency, pt.sent, utxo.vout
		FROM payout_transaction as pt
		LEFT JOIN utxo ON pt.hash = utxo.hash
		WHERE confirmed = false`)
	if err != nil {
		return err
	}

	for _, tx := range txs {
		var logger = q.log.New("hash", tx.Hash, "currency", tx.Currency)

		rpc, ok := q.getRPC(tx.Currency)
		if !ok {
			logger.Warn("Failed to grab RPC server, skipping")
			continue
		}

		// If tx has been sent, attempt to confirm it
		if tx.Sent != nil {
			hashObj, err := chainhash.NewHashFromStr(tx.Hash)
			if err != nil {
				logger.Crit("Invalid tx hash in db")
				continue
			}
			info, err := rpc.GetTxOut(hashObj, uint32(tx.Vout), true)
			if err != nil {
				logger.Error("Failed to lookup payout transaction info", "err", err)
				continue
			}

			// info is null if transaction lookup failed
			if info != nil {
				logger.Debug("Got tx info", "info", info)
				// TODO: Make this configurable
				if info.Confirmations > 6 {
					logger.Info("Marking payout transaction confirmed", "last_send", tx.Sent)
					_, err = q.db.Exec(
						`UPDATE payout_transaction SET confirmed = true WHERE hash = $1`,
						tx.Hash)
					if err != nil {
						logger.Error("Error marking payout transaction sent")
					}
				}

				// Skip sending if it's already in a block
				if info.Confirmations > 0 {
					continue
				}
			}
		}

		// If it hasn't been sent in 24 hours, and it isn't in a block yet,
		// resend it to keep it in mempools
		if tx.Sent == nil || time.Now().Sub(*tx.Sent) > time.Hour*24 {
			txObj, err := common.HexStringToTX(tx.SignedTX)
			if !ok {
				logger.Error("Failed to deser signed_tx", "err", err)
				continue
			}

			resp, err := rpc.SendRawTransaction(txObj, false)
			if err != nil {
				logger.Error("Failed sending pushed raw tx", "err", err, "tx", tx.SignedTX)
				continue
			}
			// Double check here for safety
			if resp.String() != tx.Hash {
				logger.Crit("Payout transaction mismatch, database is inconsistent!",
					"rpc_tx_hash", resp.String())
				return errors.New("Invalid state") // Abort so operator has to fix problem
			}

			_, err = q.db.Exec(
				`UPDATE payout_transaction SET sent = now() WHERE hash = $1`, tx.Hash)
			if err != nil {
				logger.Error("Error sending transaction")
			}

			logger.Info("Sent payout transaction", "last_send", tx.Sent)
		}
	}
	return nil
}
