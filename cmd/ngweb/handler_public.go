package main

import (
	"database/sql"
	sq "github.com/Masterminds/squirrel"
	"github.com/gin-gonic/gin"
	"github.com/icook/ngpool/pkg/service"
	"github.com/jmoiron/sqlx/types"
	"github.com/pkg/errors"
	"strconv"
	"time"
)

type Block struct {
	Currency string    `json:"currency"`
	Height   int64     `json:"height"`
	Hash     string    `json:"hash"`
	Status   string    `json:"status"`
	PowAlgo  string    `json:"powalgo"`
	Subsidy  int64     `json:"subsidy"`
	MinedAt  time.Time `db:"mined_at" json:"mined_at"`
	Target   float64   `db:"difficulty" json:"target"`

	Difficulty float64 `db:"-" json:"difficulty"`
}

func (q *NgWebAPI) getBlocks(c *gin.Context) {
	var blocks []*Block
	psql := sq.StatementBuilder.PlaceholderFormat(sq.Dollar)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "0"))
	base := psql.Select("currency, height, hash, powalgo, subsidy, mined_at, difficulty, status").
		From("block").OrderBy("mined_at DESC").
		Limit(100).Offset(uint64(page * 100))
	if maturity, ok := c.GetQueryArray("maturity"); ok {
		base = base.Where(sq.Eq{"status": maturity})
	}
	qstring, args, err := base.ToSql()
	q.log.Info("", "t", qstring, "args", args)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	err = q.db.Select(&blocks, qstring, args...)
	if err != nil && err != sql.ErrNoRows {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	for _, block := range blocks {
		config, ok := service.CurrencyConfig[block.Currency]
		if ok {
			block.Difficulty = config.Algo.NetDiff1 / block.Target
		}
	}
	q.apiSuccess(c, 200, res{"blocks": blocks})
}

func (q *NgWebAPI) getBlock(c *gin.Context) {
	var blockhash = c.Param("hash")

	type BlockSingle struct {
		Block
		PayoutData types.JSONText `json:"payout_data" db:"payout_data"`
		PoWHash    string         `json:"powhash" db:"powhash"`
	}
	var block BlockSingle
	err := q.db.QueryRowx(
		`SELECT
		currency, height, hash, powalgo, subsidy, mined_at, difficulty, status, payout_data, powhash
		FROM block WHERE hash = $1`, blockhash).StructScan(&block)
	if err == sql.ErrNoRows {
		q.apiError(c, 404, APIError{
			Code: "invalid_block", Title: "Block not found"})
		return
	}
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
