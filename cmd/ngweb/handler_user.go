package main

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"strconv"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcutil"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"

	"github.com/icook/ngpool/pkg/common"
	"github.com/icook/ngpool/pkg/service"
)

type PayoutAddress struct {
	Address  string `validate:"required" json:"address"`
	Currency string `validate:"required" json:"currency"`
}

type Payout struct {
	Address  string `json:"address"`
	Amount   int64  `json:"amount"`
	MinerFee int64  `db:"fee" json:"miner_fee"`

	TXID      string    `db:"hash" json:"txid"`
	Sent      time.Time `json:"sent"`
	Confirmed bool      `json:"confirmed"`
}

func (q *NgWebAPI) getPayout(c *gin.Context) {
	userID := c.GetInt("userID")
	var txhash = c.Param("hash")
	var payout Payout
	psql := sq.StatementBuilder.PlaceholderFormat(sq.Dollar)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "0"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "100"))
	base := psql.Select("p.address, p.amount, p.fee, pt.sent, pt.hash, pt.confirmed").
		From("payout as p").
		Join("payout_transaction as pt ON pt.hash = p.payout_transaction").
		OrderBy("pt.sent DESC").
		Where(sq.Eq{"p.user_id": userID, "p.payout_transaction": txhash}).
		Limit(uint64(pageSize)).Offset(uint64(page * pageSize))
	qstring, args, err := base.ToSql()
	if err == sql.ErrNoRows {
		q.apiError(c, 404, APIError{
			Code: "invalid_hash", Title: "Payout hash not found"})
		return
	}
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	err = q.db.Get(&payout, qstring, args...)
	if err != nil && err != sql.ErrNoRows {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}

	var credits = []Credit{}
	err = q.db.Select(&credits,
		`SELECT c.amount, c.sharechain, c.blockhash, b.mined_at
		FROM credit as c
		JOIN block as b ON c.blockhash = b.hash
		WHERE payout_transaction = $1
		ORDER BY b.mined_at`, txhash)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}

	q.apiSuccess(c, 200, res{"payout": payout, "credits": credits})
}

func (q *NgWebAPI) getPayouts(c *gin.Context) {
	userID := c.GetInt("userID")
	var payouts = []*Payout{}
	psql := sq.StatementBuilder.PlaceholderFormat(sq.Dollar)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "0"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "100"))
	base := psql.Select("p.address, p.amount, p.fee, pt.sent, pt.hash, pt.confirmed").
		From("payout as p").
		Join("payout_transaction as pt ON pt.hash = p.payout_transaction").
		OrderBy("pt.sent DESC").
		Where(sq.Eq{"p.user_id": userID}).
		Limit(uint64(pageSize)).Offset(uint64(page * pageSize))
	qstring, args, err := base.ToSql()
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	err = q.db.Select(&payouts, qstring, args...)
	if err != nil && err != sql.ErrNoRows {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	q.apiSuccess(c, 200, res{"payouts": payouts})
}

func (q *NgWebAPI) getMe(c *gin.Context) {
	userID := c.GetInt("userID")
	user := make(map[string]interface{})
	err := q.db.QueryRowx(
		"SELECT id, email, username FROM users WHERE id = $1",
		userID).MapScan(user)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}

	var payoutAddrs []PayoutAddress
	err = q.db.Select(&payoutAddrs,
		`SELECT currency, address FROM payout_address WHERE user_id = $1`, userID)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	addrMap := map[string]string{}
	for _, currency := range service.CurrencyConfig {
		addrMap[currency.Code] = ""
	}
	for _, addr := range payoutAddrs {
		addrMap[addr.Currency] = addr.Address
	}
	q.apiSuccess(c, 200, res{"user": user, "payout_addresses": addrMap})
}

func (q *NgWebAPI) postSetPayout(c *gin.Context) {
	var req PayoutAddress
	if !q.BindValid(c, &req) {
		return
	}
	userID := c.GetInt("userID")
	config, ok := service.CurrencyConfig[req.Currency]
	if !ok {
		q.apiError(c, 400, APIError{
			Code:  "invalid_currency",
			Title: "No currency with that code"})
		return
	}
	_, err := btcutil.DecodeAddress(req.Address, config.Params)
	if err != nil {
		q.apiError(c, 400, APIError{
			Code:  "invalid_address",
			Title: "Address given is not valid for that network"})
		return
	}
	_, err = q.db.Exec(
		`INSERT INTO payout_address
		(address, currency, user_id)
		VALUES ($1, $2, $3) ON CONFLICT (user_id, currency) DO UPDATE
		SET address = $1`,
		req.Address, req.Currency, userID)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	c.Status(200)
}

func (q *NgWebAPI) getCreatePayout(c *gin.Context) {
	var currency = c.Param("currency")

	config, ok := service.CurrencyConfig[currency]
	if !ok {
		q.apiError(c, 400, APIError{
			Code:  "unrecognized_currency",
			Title: "Specified currency code is not configured"})
		return
	}

	rpc, ok := q.getRPC(currency)
	if !ok {
		q.log.Warn("Failed to grab RPC server", "currency", currency)
		q.apiError(c, 500, APIError{
			Code:  "rpc_failure",
			Title: "RPC is currency unavailable"})
		return
	}

	type Credit struct {
		ID      int
		UserID  int `db:"user_id"`
		Amount  int64
		Address string
	}
	var credits []Credit
	err := q.db.Select(&credits,
		`SELECT credit.id, credit.user_id, credit.amount, payout_address.address
		FROM credit LEFT JOIN payout_address ON
		credit.user_id = payout_address.user_id AND payout_address.currency = $1
		WHERE credit.currency = $2 AND payout_address.address IS NOT NULL
		AND credit.payout_transaction IS NULL`, currency, currency)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	if len(credits) == 0 {
		q.apiSuccess(c, 200, res{})
		return
	}
	var maps = map[int]*common.PayoutMap{}
	var totalPayout int64 = 0
	defaultNet := &chaincfg.MainNetParams
	for _, credit := range credits {
		// Add to a datastructure to pass to signer that provides metadata for
		// an output
		pm, ok := maps[credit.UserID]
		if !ok {
			// Add to our list of Outputs
			addr, err := btcutil.DecodeAddress(credit.Address, defaultNet)
			// TODO: consider handling this more elegantly by ignoring invalid addresses
			if err != nil {
				q.apiException(c, 500, errors.WithStack(err), APIError{
					Code:  "invalid_address",
					Title: "One or more payout addresses are invalid"})
				return
			}
			pm = &common.PayoutMap{
				UserID:     credit.UserID,
				Address:    credit.Address,
				AddressObj: addr,
			}
			maps[credit.UserID] = pm
		}
		pm.Amount += credit.Amount
		pm.CreditIDs = append(pm.CreditIDs, credit.ID)

		totalPayout += credit.Amount
	}
	q.log.Info("Credits accumulated",
		"credit_count", len(credits),
		"payout_map_count", len(maps),
		"total", totalPayout)

	// Pick our UTXOs for the transaction. For now just pick whatever...
	var utxos []common.UTXO
	err = q.db.Select(&utxos,
		`SELECT hash, vout, amount, address FROM utxo
		WHERE currency = $1 AND spendable = true AND spent = false`, currency)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	var selectedUTXO []common.UTXO
	var totalPaid int64 = 0
	inputs := []btcjson.TransactionInput{}
	for _, utxo := range utxos {
		// This is unintuitive. To make the raw transaction we need our hash
		// encoded in RPC byte order, so we reverse it. We also reverse the
		// hash we send to the signer, since it's going to lookup the address
		// via the prevout hash
		hexHsh, _ := hex.DecodeString(utxo.Hash)
		common.ReverseBytes(hexHsh)
		utxo.Hash = hex.EncodeToString(hexHsh)

		selectedUTXO = append(selectedUTXO, utxo)

		inputs = append(inputs, btcjson.TransactionInput{
			Txid: utxo.Hash,
			Vout: uint32(utxo.Vout),
		})
		totalPaid += utxo.Amount
		if totalPaid >= totalPayout {
			break
		}
	}
	q.log.Info("Picked UTXOs", "utxo_count", len(inputs), "total", totalPaid)
	if totalPaid < totalPayout {
		q.log.Warn("Insufficient funds for payout",
			"currency", currency,
			"utxosum", totalPaid,
			"payoutsum", totalPayout)
		q.apiError(c, 500, APIError{
			Code:  "insufficient_funds",
			Title: "Not enough utxos to pay"})
		return
	}

	// Encode maps to outputs
	var amounts = map[btcutil.Address]btcutil.Amount{}
	for _, pm := range maps {
		amounts[pm.AddressObj] = btcutil.Amount(pm.Amount)
	}
	// Add change address to outputs
	change := totalPaid - totalPayout
	q.log.Info("Change calculated", "change", change)
	if change > 0 {
		amounts[*config.BlockSubsidyAddress] += btcutil.Amount(change)
	}

	// Create our transaction
	locktime := int64(0)
	tx, err := rpc.CreateRawTransaction(inputs, amounts, nil)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), APIError{
			Code:  "rpc_failure",
			Title: "RPC failed to run CreateRawTransaction"})
		return
	}

	// Compute the fee, then extract it from each of the amounts...
	size := tx.SerializeSize() + int(float32(106.5)*float32(len(selectedUTXO)))
	fee := size * config.PayoutTransactionFee
	q.log.Info("Generated tx for fee calc", "tx", tx)
	feePerPayee := fee / len(maps)
	q.log.Info("Calculating fee for payout",
		"fee per byte", config.PayoutTransactionFee,
		"tx size", size,
		"total fee", fee,
		"fee per payee", feePerPayee)

	// Remove the fee from each payee proportional to their payout
	amounts = map[btcutil.Address]btcutil.Amount{}
	for _, pm := range maps {
		fract := float64(pm.Amount) / float64(totalPaid)
		feePortion := int64(fract * float64(fee))
		amounts[pm.AddressObj] = btcutil.Amount(int64(pm.Amount) - feePortion)
		pm.MinerFee = feePortion
	}
	// readd the change
	if change > 0 {
		amounts[*config.BlockSubsidyAddress] += btcutil.Amount(change)
	}

	var realFee int64 = totalPaid
	for _, out := range amounts {
		realFee -= int64(out)
	}
	q.log.Info("Real fee", "fee", realFee, "perc", float64(realFee)/float64(totalPaid)*100)

	tx, err = rpc.CreateRawTransaction(inputs, amounts, &locktime)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), APIError{
			Code:  "rpc_failure",
			Title: "RPC failed to run CreateRawTransaction"})
		return
	}

	txWriter := bytes.Buffer{}
	err = tx.SerializeNoWitness(&txWriter)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), APIError{
			Code:  "tx_serialize_err",
			Title: "Transaction serialization failed"})
		return
	}

	q.apiSuccess(c, 200, res{
		"payout_meta": common.PayoutMeta{
			PayoutMaps:    maps,
			ChangeAddress: (*config.BlockSubsidyAddress).EncodeAddress(),
		},
		"tx":     hex.EncodeToString(txWriter.Bytes()),
		"inputs": selectedUTXO,
	})
}

func (q *NgWebAPI) postPayout(c *gin.Context) {
	type Payout struct {
		PayoutMeta common.PayoutMeta `json:"payout_meta"`
		TX         string
		Currency   string
	}
	var req Payout
	if !q.BindValid(c, &req) {
		return
	}

	// TODO: Extract this into common function like BindValid
	config, ok := service.CurrencyConfig[req.Currency]
	if !ok {
		q.apiError(c, 400, APIError{
			Code:  "invalid_currency",
			Title: "No currency with that code"})
		return
	}

	payoutTx, err := common.HexStringToTX(req.TX)
	if err != nil {
		q.apiException(c, 500, err, APIError{
			Code:  "invalid_tx",
			Title: "TX is not valid"})
		return
	}

	// Simple sanity check. +1 is for the change address, which isn't
	// represented by a payoutmap
	if len(payoutTx.TxOut) != len(req.PayoutMeta.PayoutMaps)+1 {
		q.apiException(c, 500, err, APIError{
			Code:  "output_mismatch",
			Title: "SignedTX outputs dont match payout maps"})
		return
	}
	payoutTxHash := payoutTx.TxHash().String()

	// Insert PayoutTransaction, UTXO (change), and Payout entries for every user
	tx, err := q.db.Begin()
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}

	// Insert all change UTXOs. Should always be just 1, unless a user is being
	// paid out to the BlockSubsidyAddress
	var inserted = false
	for idx, out := range payoutTx.TxOut {
		_, addrs, count, err := txscript.ExtractPkScriptAddrs(out.PkScript, config.Params)
		if err != nil || count != 1 {
			tx.Rollback()
			q.apiException(c, 500, err, APIError{
				Code:  "invalid_output",
				Title: "An output had unexpected script"})
			return
		}
		address := addrs[0].EncodeAddress()
		if address != req.PayoutMeta.ChangeAddress {
			// Skip this output, it's for a customer, not a change UTXO
			continue
		}

		inserted = true
		_, err = tx.Exec(
			`INSERT INTO utxo (hash, vout, amount, currency, address)
			VALUES ($1, $2, $3, $4, $5)`,
			payoutTxHash,
			idx,
			out.Value,
			config.Code,
			address)
		if err != nil {
			q.apiException(c, 500, errors.WithStack(err), SQLError)
			return
		}
	}
	// We should always have a change output, so abort if this sanity check
	// doesn't pass
	if inserted == false {
		tx.Rollback()
		q.apiException(c, 500, errors.WithStack(err), APIError{
			Code:  "no_change",
			Title: "Transaction had no change address"})
		return
	}

	_, err = tx.Exec(
		`INSERT INTO payout_transaction
		(hash, signed_tx, currency) VALUES ($1, decode($2, 'hex'), $3)`,
		payoutTxHash, req.TX, config.Code)
	if err != nil {
		tx.Rollback()
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}

	for _, pm := range req.PayoutMeta.PayoutMaps {
		_, err = tx.Exec(
			`INSERT INTO payout
			(user_id, amount, payout_transaction, fee, address)
			VALUES ($1, $2, $3, $4, $5)`,
			pm.UserID, pm.Amount, payoutTxHash, pm.MinerFee, pm.Address)
		if err != nil {
			tx.Rollback()
			q.apiException(c, 500, errors.WithStack(err), SQLError)
			return
		}

		psql := sq.StatementBuilder.PlaceholderFormat(sq.Dollar)
		qstring, args, err := psql.Update("credit").
			Set("payout_transaction", payoutTxHash).
			Where(sq.Eq{"id": pm.CreditIDs}).ToSql()
		if err != nil {
			tx.Rollback()
			q.apiException(c, 500, errors.WithStack(err), SQLError)
			return
		}
		_, err = tx.Exec(qstring, args...)
		if err != nil {
			tx.Rollback()
			q.apiException(c, 500, errors.WithStack(err), SQLError)
			return
		}
	}

	// We're commiting everything to the database before sending to ensure
	// against a double payout scenario. ngsigner will never try to payout
	// credits with populated payout_transaction attributes, so we make sure to
	// populate them (and commit them successfully) before sending the money to
	// avoid a double spend
	err = tx.Commit()
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}

	c.Status(200)

	err = q.updatePayoutTransactions()
	if err != nil {
		q.log.Error("Failed to send txs after postPayout", "err", err)
	}
}
