package service

import (
	"encoding/hex"
	"fmt"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/mitchellh/mapstructure"
	//	"github.com/satori/go.uuid.git"
	log "github.com/inconshreveable/log15"
)

type ChainConfigDecoder struct {
	Code           string
	SubsidyAddress string
	FeeAddress     string
	PowAlgorithm   string

	PubKeyAddrID  string
	PrivKeyID     string
	PrivKeyAddrID string
	NetMagic      uint32
}

type ChainConfig struct {
	Code                string
	Algo                *Algo
	Params              *chaincfg.Params
	BlockSubsidyAddress *btcutil.Address
	FeeAddress          *btcutil.Address
}

var CurrencyConfig = map[string]*ChainConfig{}

func (s *Service) SetupCurrencies() {
	for _, rawConfig := range s.config.GetStringMap("Currencies") {
		var config ChainConfigDecoder
		err := mapstructure.Decode(rawConfig, &config)
		if err != nil {
			panic(err)
		}
		log.Debug("Decoded currency config", "config", config, "rawConfig", rawConfig)
		fmt.Printf("%#v", rawConfig)
		fmt.Printf("%#v", err)

		params := &chaincfg.Params{
			Name: config.Code,
			Net:  wire.BitcoinNet(config.NetMagic),
		}

		decoded, err := hex.DecodeString(config.PrivKeyAddrID)
		if err != nil {
			panic(err)
		}
		params.PrivateKeyID = decoded[0]

		decoded, err = hex.DecodeString(config.PubKeyAddrID)
		if err != nil {
			panic(err)
		}
		params.PubKeyHashAddrID = decoded[0]

		bsa, err := btcutil.DecodeAddress(config.SubsidyAddress, params)
		if err != nil {
			panic(err)
		}

		fa, err := btcutil.DecodeAddress(config.FeeAddress, params)
		if err != nil {
			panic(err)
		}

		cc := &ChainConfig{
			Code:                config.Code,
			Params:              params,
			BlockSubsidyAddress: &bsa,
			FeeAddress:          &fa,
			Algo:                AlgoConfig[config.PowAlgorithm],
		}

		if err := chaincfg.Register(params); err != nil {
			panic("failed to register network: " + err.Error())
		}
		CurrencyConfig[config.Code] = cc
	}
}
