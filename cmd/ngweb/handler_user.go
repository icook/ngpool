package main

import (
	"bytes"
	"encoding/hex"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil"
	"github.com/gin-gonic/gin"
	"github.com/icook/ngpool/pkg/service"
	"github.com/pkg/errors"
)

type PayoutAddress struct {
	Address  string `validate:"required" json:"address"`
	Currency string `validate:"required" json:"currency"`
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
		WHERE credit.currency = $2 AND payout_address.address IS NOT NULL`, currency, currency)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	if len(credits) == 0 {
		q.apiSuccess(c, 200, res{})
		return
	}
	type PayoutMap struct {
		CreditIDs  []int
		UserID     int
		AddressObj btcutil.Address `json:"-"`
		Address    string
		Amount     int64
		MinerFee   int64
	}
	var maps = map[int]*PayoutMap{}
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
			pm = &PayoutMap{
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
	type UTXO struct {
		Hash   string
		Vout   int
		Amount int64
	}
	var utxos []UTXO
	err = q.db.Select(&utxos,
		`SELECT hash, vout, amount FROM utxo
		WHERE currency = $1 AND spendable = true AND spent = false`, currency)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	var selectedUTXO []UTXO
	var totalPaid int64 = 0
	inputs := []btcjson.TransactionInput{}
	for _, utxo := range utxos {
		selectedUTXO = append(selectedUTXO, utxo)
		inputs = append(inputs, btcjson.TransactionInput{
			Txid: utxo.Hash, Vout: uint32(utxo.Vout)})
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
	fee := tx.SerializeSize() * config.PayoutTransactionFee
	q.log.Info("Generated tx for fee calc", "tx", tx)
	feePerPayee := fee / len(maps)
	q.log.Info("Calculating fee for payout",
		"fee per byte", config.PayoutTransactionFee,
		"tx size", tx.SerializeSize(),
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
	err = tx.Serialize(&txWriter)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), APIError{
			Code:  "tx_serialize_err",
			Title: "Transaction serialization failed"})
		return
	}

	q.apiSuccess(c, 200, res{"payout_maps": maps, "tx": hex.EncodeToString(txWriter.Bytes())})
}
