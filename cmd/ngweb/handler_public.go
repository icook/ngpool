package main

import (
	"database/sql"
	sq "github.com/Masterminds/squirrel"
	"github.com/gin-gonic/gin"
	"github.com/icook/ngpool/pkg/service"
	"github.com/jmoiron/sqlx/types"
	"github.com/pkg/errors"
	"strconv"
	"strings"
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
	Target   float64   `json:"target"`

	Difficulty float64 `json:"difficulty"`
}

type Credit struct {
	Blockhash  string    `json:"blockhash,omitempty"`
	Currency   string    `json:"currency,omitempty"`
	Username   string    `json:"username,omitempty"`
	UserID     int       `json:"user_id,omitempty" db:"user_id"`
	Amount     int64     `json:"amount"`
	ShareChain string    `json:"sharechain"`
	MinedAt    time.Time `db:"mined_at" json:"mined_at"`
}

func (q *NgWebAPI) getBlocks(c *gin.Context) {
	var blocks = []*Block{}
	psql := sq.StatementBuilder.PlaceholderFormat(sq.Dollar)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "0"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "100"))
	base := psql.Select("currency, height, hash, powalgo, subsidy, mined_at, target, status").
		From("block").OrderBy("mined_at DESC").
		Limit(uint64(pageSize)).Offset(uint64(page * pageSize))
	if maturity, ok := c.GetQuery("maturity"); ok && maturity != "" {
		base = base.Where(sq.Eq{"status": strings.Split(maturity, ",")})
	}
	if powalgo, ok := c.GetQuery("powalgo"); ok && powalgo != "" {
		base = base.Where(sq.Eq{"powalgo": strings.Split(powalgo, ",")})
	}
	if currency, ok := c.GetQuery("currency"); ok && currency != "" {
		base = base.Where(sq.Eq{"currency": strings.Split(currency, ",")})
	}
	qstring, args, err := base.ToSql()
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
		Credited   bool           `json:"credited"`
	}
	var block BlockSingle
	err := q.db.QueryRowx(
		`SELECT
		currency, height, hash, powalgo, subsidy, mined_at, target, status,
		payout_data, powhash, credited
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

	var credits = []Credit{}
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

func (q *NgWebAPI) getCommon(c *gin.Context) {
	q.apiSuccess(c, 200, res{
		"raw_currencies": service.RawCurrencyConfig,
		"currencies":     service.CurrencyConfig,
		"algos":          service.AlgoConfig,
	})
}

func (q *NgWebAPI) getCoinservers(c *gin.Context) {
	q.coinserversMtx.RLock()
	q.apiSuccess(c, 200, res{
		"coinservers": q.coinservers,
	})
	q.coinserversMtx.RUnlock()
}
