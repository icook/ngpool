package main

import (
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil"
)

type ChainConfig struct {
	Params              *chaincfg.Params
	BlockSubsidyAddress *btcutil.Address
	FeeAddress          *btcutil.Address
}

func NewChainConfig(feeAddress string, blockSubsidyAddress string,
	params *chaincfg.Params) *ChainConfig {
	if err := chaincfg.Register(params); err != nil {
		panic("failed to register network: " + err.Error())
	}

	bsa, err := btcutil.DecodeAddress(blockSubsidyAddress, params)
	if err != nil {
		panic(err)
	}

	fa, err := btcutil.DecodeAddress(feeAddress, params)
	if err != nil {
		panic(err)
	}

	return &ChainConfig{
		Params:              params,
		BlockSubsidyAddress: &bsa,
		FeeAddress:          &fa,
	}
}

var CurrencyConfig map[string]*ChainConfig

func init() {
	CurrencyConfig = map[string]*ChainConfig{
		"DOGE_T": NewChainConfig(
			"nZgyAMoJ9viU4qnE1gnUvxUxQZdQ8YbB9E",
			"nZgyAMoJ9viU4qnE1gnUvxUxQZdQ8YbB9E",
			&chaincfg.Params{
				PubKeyHashAddrID: 0x71,
				PrivateKeyID:     0xf1,
			}),
	}
}
