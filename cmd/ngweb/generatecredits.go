package main

import (
	"github.com/icook/portabledec"
	log "github.com/inconshreveable/log15"
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

func (q *NgWebAPI) GenerateCredits() {
	type Block struct {
		currency   string
		height     int64
		hash       string
		powhash    string
		subsidy    portabledec.Decimal
		mined_at   time.Time
		mined_by   time.Time
		difficulty float64
	}
	var blocks []Block
	err := q.db.Select(&blocks, "SELECT * FROM block WHERE credited = false")
	if err != nil {
		log.Crit("Failed to select blocks", "err", err)
		return
	}
	// for _, block := range blocks {
	// }
}
