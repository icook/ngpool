package main

import (
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx/types"
	"github.com/pkg/errors"
	"time"
)

type Block struct {
	Currency   string    `json:"currency"`
	Height     int64     `json:"height"`
	Hash       string    `json:"hash"`
	Status     string    `json:"status"`
	PowAlgo    string    `json:"powalgo"`
	Subsidy    int64     `json:"subsidy"`
	MinedAt    time.Time `db:"mined_at" json:"mined_at"`
	Difficulty float64   `json:"difficulty"`
}

func (q *NgWebAPI) getBlock(c *gin.Context) {
	var blocks []Block
	err := q.db.Select(&blocks,
		`SELECT
		currency, height, hash, powalgo, subsidy, mined_at, difficulty, status
		FROM block LIMIT 100`)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	q.apiSuccess(c, 200, res{"blocks": blocks})
}

func (q *NgWebAPI) getBlocks(c *gin.Context) {
	var blockhash = c.Param("hash")

	type BlockSingle struct {
		Block
		PayoutData types.JSONText `json:"payout_data" db:"payout_data"`
	}
	var block BlockSingle
	err := q.db.QueryRowx(
		`SELECT
		currency, height, hash, powalgo, subsidy, mined_at, difficulty, status, payout_data
		FROM block WHERE hash = $1`, blockhash).StructScan(&block)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}

	type Credit struct {
		Username   string `json:"username"`
		UserID     int    `json:"user_id" db:"user_id"`
		Amount     int64  `json:"amount"`
		ShareChain string `json:"sharechain"`
	}
	var credits []Credit
	err = q.db.Select(&credits,
		`SELECT users.username, credit.user_id, credit.amount, credit.sharechain
		FROM credit LEFT JOIN users ON credit.user_id = users.id
		WHERE credit.blockhash = $1`, blockhash)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}

	q.apiSuccess(c, 200, res{"block": block, "credits": credits})
}
