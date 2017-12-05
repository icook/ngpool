package main

import (
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil"
)

type ChainConfig struct {
	Code                string
	Params              *chaincfg.Params
	BlockSubsidyAddress *btcutil.Address
	FeeAddress          *btcutil.Address
}

func NewChainConfig(code string, feeAddress string, blockSubsidyAddress string,
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

	cc := &ChainConfig{
		Code:                code,
		Params:              params,
		BlockSubsidyAddress: &bsa,
		FeeAddress:          &fa,
	}
	CurrencyConfig[code] = cc
	return cc
}

var CurrencyConfig = map[string]*ChainConfig{}

func init() {
	NewChainConfig(
		"DOGE_T",
		"nZgyAMoJ9viU4qnE1gnUvxUxQZdQ8YbB9E",
		"nZgyAMoJ9viU4qnE1gnUvxUxQZdQ8YbB9E",
		&chaincfg.Params{
			PubKeyHashAddrID: 0x71,
			PrivateKeyID:     0xf1,
		})
}
