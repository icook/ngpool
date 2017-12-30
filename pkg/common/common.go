package common

import (
	"github.com/btcsuite/btcutil"
)

// A type we use to pass payout transaction metadata between ngweb and ngsigner
type PayoutMap struct {
	CreditIDs []int
	UserID    int
	Address   string
	Amount    int64
	MinerFee  int64

	AddressObj btcutil.Address `json:"-"`
}

// Pass UTXO information (address mostly) to ngsigner for signing. Also for
// decoding from database
type UTXO struct {
	Hash    string
	Vout    uint32
	Amount  int64
	Address string
}

func ReverseBytes(s []byte) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}