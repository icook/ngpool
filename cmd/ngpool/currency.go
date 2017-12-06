package main

import (
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil"
	"golang.org/x/crypto/scrypt"
)

type HashFunc func(input []byte) ([]byte, error)

func scryptHash(input []byte) ([]byte, error) {
	return scrypt.Key(input, input, 1024, 1, 1, 32)
}

type ChainConfig struct {
	Code                string
	PoWHash             HashFunc
	Params              *chaincfg.Params
	BlockSubsidyAddress *btcutil.Address
	FeeAddress          *btcutil.Address
}

func NewChainConfig(code string, feeAddress string, blockSubsidyAddress string,
	powFunc HashFunc, params *chaincfg.Params) *ChainConfig {
	params.Name = code

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
		PoWHash:             powFunc,
	}
	CurrencyConfig[code] = cc
	return cc
}

var CurrencyConfig = map[string]*ChainConfig{}

func init() {
	NewChainConfig(
		"LTC_T",
		"mucHkBoHAF8DTQWFuwQXHiewqi3ZBDNNWh",
		"mucHkBoHAF8DTQWFuwQXHiewqi3ZBDNNWh",
		scryptHash,
		&chaincfg.Params{
			PubKeyHashAddrID: 0x6F,
			PrivateKeyID:     0xEF,
			Net:              0xfdd2c8f1,
		})
	NewChainConfig(
		"DOGE_T",
		"nZgyAMoJ9viU4qnE1gnUvxUxQZdQ8YbB9E",
		"nZgyAMoJ9viU4qnE1gnUvxUxQZdQ8YbB9E",
		scryptHash,
		&chaincfg.Params{
			PubKeyHashAddrID: 0x71,
			PrivateKeyID:     0xf1,
			Net:              0xfabfb5da,
		})
}
