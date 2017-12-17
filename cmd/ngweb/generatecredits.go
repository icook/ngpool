package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/icook/ngpool/pkg/service"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"time"
)

func init() {
	payoutCmd := &cobra.Command{
		Use: "common",
		Run: func(cmd *cobra.Command, args []string) {
			ng := NewNgWebAPI()
			ng.ParseConfig()
			ng.GenerateCredits()
		},
	}
	RootCmd.AddCommand(payoutCmd)
}

type Block struct {
	Currency   string
	Height     int64
	Hash       string
	PowAlgo    string
	Subsidy    int64
	MinedAt    time.Time `db:"mined_at"`
	Difficulty float64

	algoConfig    *service.Algo
	lastBlockTime time.Time
}

type ShareChainPayout struct {
	// Loaded from SQL GROUP BY
	Difficulty float64
	Name       string `db:"sharechain"`

	// Values used for computing user payouts
	config         *service.ShareChainConfig
	subsidy        int64
	subsidyPayable int64
	subsidyFee     int64
	data           map[string]interface{}
}

type Credit struct {
	// Loaded from SQL GROUP BY
	Difficulty float64
	Amount     int64
	Fee        float64
	UserID     int `db:"user_id"`
}

func (q *NgWebAPI) processBlock(block *Block) error {
	q.log.Info("Starting payout", "block", block)
	// Get all the shares involced in the block solve by chain. This number is
	// used to split the block reward between share chains proportionally for
	// their effort
	// =====
	// get last block solve time
	err := q.db.QueryRowx(
		`SELECT mined_at FROM block
		WHERE height < $1 AND currency = $2
		ORDER BY height DESC`,
		block.Height, block.Currency).Scan(&block.lastBlockTime)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	q.log.Debug("Got last block time", "time", block.lastBlockTime)

	// get share count for each chain
	var sharechains []*ShareChainPayout
	err = q.db.Select(&sharechains,
		`SELECT sharechain, 
		SUM (difficulty) as difficulty
		FROM share 
		WHERE mined_at > $1 AND mined_at < $2 AND currencies @> $3
		GROUP BY sharechain`,
		block.lastBlockTime, block.MinedAt, pq.StringArray([]string{block.Currency}))
	if err != nil {
		return err
	}
	// Lookup the config for each chain
	for _, sc := range sharechains {
		config, ok := service.ShareChain[sc.Name]
		if !ok {
			return errors.Errorf("Unknown ShareChain %s", sc.Name)
		}
		sc.config = config
		q.log.Info("Loaded ShareChainConfig", "config", config)
	}
	var shareChainsTotal float64 = 0
	for _, sc := range sharechains {
		shareChainsTotal += sc.Difficulty
	}
	var totalCredited int64 = 0
	for _, sc := range sharechains {
		sc.subsidy = int64((sc.Difficulty / shareChainsTotal) * float64(block.Subsidy))
		totalCredited += sc.subsidy
	}
	// Give the rounded satoshi to the first sharechain, it won't ever be much
	// (if any). This keeps accounting clean
	if totalCredited > block.Subsidy {
		return errors.New("Float math rounding overflow")
	}
	rounded := block.Subsidy - totalCredited
	q.log.Debug("Giving rounded sharechain remainder",
		"remainder", rounded, "sharechain", sharechains[0].Name)
	sharechains[0].subsidy += rounded

	// Calculate fees for all chains and run payout function
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}
	for _, sc := range sharechains {
		sc.subsidyFee = int64(sc.config.Fee * float64(sc.subsidy))
		sc.subsidyPayable = sc.subsidy - sc.subsidyFee
		fmt.Printf("%+v\n", sc)
		var credits []*Credit
		switch sc.config.PayoutMethod {
		case "pplns":
			credits, err = q.payoutPPLNS(sc, block)
			if err != nil {
				return err
			}
		}
		for _, c := range credits {
			_, err = tx.Exec(
				`INSERT INTO credit
				(user_id, amount, currency, blockhash, sharechain)
				VALUES ($1, $2, $3, $4, $5)`,
				c.UserID, c.Amount, block.Currency, block.Hash, sc.Name)
			if err != nil {
				tx.Rollback()
				return err
			}
		}
	}

	// This structure will get loaded into the database after payout. It's
	// visible on the frontend to help users and admins understand how payouts
	// are operating, debugging, and testing
	payoutData := map[string]interface{}{
		"credited_at":                time.Now(),
		"sharechain_round_amount":    rounded,
		"sharechain_round_recipient": sharechains[0].Name,
		"sharechains":                sharechains,
		"sharechain_total":           shareChainsTotal,
		"last_block_time":            block.lastBlockTime,
	}
	serial, err := json.Marshal(payoutData)
	if err != nil {
		tx.Rollback()
		return err
	}
	result, err := tx.Exec(
		`UPDATE block SET credited = true, payout_data = $1
		WHERE hash = $2`,
		serial, block.Hash)
	if err != nil {
		tx.Rollback()
		return err
	}
	affect, err := result.RowsAffected()
	if err == nil && affect == 0 {
		tx.Rollback()
		return errors.New("Failed to update block information")
	}

	err = tx.Commit()
	if err != nil {
		return err
	}

	return nil
}

func (q *NgWebAPI) payoutPPLNS(sc *ShareChainPayout, block *Block) ([]*Credit, error) {
	sharesToFind, acc := block.algoConfig.Diff1SharesForDiff(block.Difficulty)
	// Static last N of 2 for now TODO: Make this configurable
	var n float64 = 2
	sharesToFind *= n
	// Static fee user id, needs to be configurable as well
	feeUserID := 1
	q.log.Info("Calculated required shares",
		"accuracy", acc, "requiredShares", sharesToFind, "diff", block.Difficulty, "diff1", block.algoConfig.Diff1)

	userShares, total, err := q.collectShares(sharesToFind, feeUserID, sc.Name, block.MinedAt)
	if err != nil {
		return nil, err
	}
	sc.data = map[string]interface{}{
		"type":         "pplns",
		"n":            n,
		"diff1":        block.algoConfig.Diff1,
		"sharesToFind": sharesToFind,
		"sharesFound":  total,
	}

	q.log.Info("Computing credits for users")
	var credits []*Credit
	for userID, shares := range userShares {
		fract := shares / total
		c := &Credit{
			UserID:     userID,
			Difficulty: shares,
			Amount:     int64(float64(sc.subsidyPayable) * fract),
			Fee:        float64(sc.subsidyFee) * fract,
		}
		fmt.Printf("%+v\n", c)
		credits = append(credits, c)
	}
	return credits, nil
}

func (q *NgWebAPI) collectShares(shareCount float64, feeUserID int,
	shareChainName string, start time.Time) (map[int]float64, float64, error) {
	var (
		accumulatedShares float64 = 0
		userShares                = map[int]float64{}
		selectOffset              = 0
	)
	type Share struct {
		Difficulty float64
		UserID     *int `db:"id"`
	}
	for {
		var shares []Share
		err := q.db.Select(&shares,
			`SELECT share.difficulty, users.id FROM share
			LEFT JOIN users ON users.username = share.username
			WHERE share.mined_at < $1 AND share.sharechain = $2
			ORDER BY share.mined_at DESC
			LIMIT 100 OFFSET $3`,
			start, shareChainName, selectOffset)
		if err != nil && err != sql.ErrNoRows {
			return nil, 0, err
		}
		if len(shares) == 0 {
			q.log.Info("Exiting share collection, no more shares")
			break
		}

		for _, share := range shares {
			// If we couldn't match a user from the share, give it to the fee
			// user
			if share.UserID == nil {
				share.UserID = &feeUserID
			}
			userShares[*share.UserID] += share.Difficulty

			// Exit if we have the amount of shares we need
			accumulatedShares += share.Difficulty
			if accumulatedShares >= shareCount {
				// TODO: With very large share difficulties and low block diff
				// we might have unbalanced, we should remove the excess ideally
				return userShares, accumulatedShares, nil
			}
		}
		selectOffset += 100
	}
	return userShares, accumulatedShares, nil
}

func (q *NgWebAPI) GenerateCredits() error {
	var blocks []Block
	err := q.db.Select(&blocks,
		`SELECT currency, height, hash, powalgo, subsidy, mined_at, difficulty
		FROM block WHERE credited = false AND mature = true`)
	if err != nil {
		return err
	}
	for _, block := range blocks {
		config, ok := service.AlgoConfig[block.PowAlgo]
		if !ok {
			return errors.Errorf("Couldn't locate pow alogo %s", block.PowAlgo)
		}
		block.algoConfig = config
		q.log.Debug("Loaded AlgoConfig", "config", config, "block", block.Hash)

		err := q.processBlock(&block)
		if err != nil {
			return err
		}
	}
	return nil
}
