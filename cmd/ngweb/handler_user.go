package main

import (
	"github.com/btcsuite/btcutil"
	"github.com/gin-gonic/gin"
	"github.com/icook/ngpool/pkg/service"
	"github.com/pkg/errors"
)

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
	q.apiSuccess(c, 200, res{"user": user})
}

type PayoutAddress struct {
	Address  string `validate:"required" json:"address"`
	Currency string `validate:"required" json:"currency"`
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

func (q *NgWebAPI) getPayout(c *gin.Context) {
	userID := c.GetInt("userID")
	var payoutAddrs []PayoutAddress
	err := q.db.Select(&payoutAddrs,
		`SELECT currency, address FROM payout_address WHERE user_id = $1`, userID)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	q.apiSuccess(c, 200, res{"payout_addresses": payoutAddrs})
}
