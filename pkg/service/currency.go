package service

import (
	"encoding/hex"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	log "github.com/inconshreveable/log15"
	"github.com/mitchellh/mapstructure"
	"os"
)

// This is the structure for the config file represnetation of a "ChainConfig".
// Many of these properties get parsed into special datastructures for easier
// use later
type ChainConfigDecoder struct {
	// Pass through - these are passed through to ChainConfig unmodified

	// The currency code. These are canoncially differentiated "LTC_T" for a
	// litecoin testnet, or "LTC_R" for a litecoin regtest network, since this
	// Code technically encodes the network type as well. It must be unique.
	Code string
	// Number of confirmations required before coinbase UTXOs are allowed to be
	// spent. This is a network rule that varies per-currency. This is required
	// to know when we can payout credits to users. If set too low,
	// transactions will fail to be confirmed by the network
	BlockMatureConfirms int64
	// If this currency is merge mined, should we flush stratum miner jobs when
	// a new block is announced? This should be selected based on the cost of a
	// work restart (in stale shares), and the value of merge mined currency.
	// If the merge mined currency is worth 1/1000th of the main chain
	// currency, probably leave this false. If they are close in value,
	// consider setting it to true
	FlushAux bool
	// This is the transaction fee to use for payouts. Given in satoshis / byte
	PayoutTransactionFee int

	// Parsed - These options get parsed in SetupCurrencies

	// The address to send newly mined coins
	SubsidyAddress string
	// The address to payout accumulated fees to
	FeeAddress string
	// The name of an algorithm. Current options are scrypt
	PowAlgorithm string

	// These parameters are for github.com/btcsuite/btcd/chaincfg.Params, a
	// datastructure that btcd's libraries pass around to do network specific
	// operations. We use btcd extensively to handle bitcoin-like data
	// structures

	// Address Version (pubkey prefix) given in hex (1 byte)
	PubKeyAddrID string
	// Private key version for Wallet Import Format (WIF) given in hex (1 byte)
	PrivKeyID string
	// Private key version for Wallet Import Format (WIF) given in hex (1 byte)
	PrivKeyAddrID string
	// The p2p message magic bytes. This is a sequence of 4 bytes that allow
	// bitcoin to reject connections from litecoin nodes, etc. It is sent at
	// the beginning of every message on bitcoin p2p networks, and is unique to
	// the currency and network
	NetMagic uint32
}

// This encodes network rules and pool wide preferences for handling of that
// currency. There would be a different one of these for testnet, mainnet, or
// regtest blockchains for a single currency.
type ChainConfig struct {
	Code                 string
	BlockMatureConfirms  int64
	FlushAux             bool
	PayoutTransactionFee int

	Algo                *Algo
	Params              *chaincfg.Params
	BlockSubsidyAddress *btcutil.Address
	FeeAddress          *btcutil.Address
}

// This is a global lookup for currency information. All programs load "common"
// configuration on start and populate this by calling "SetupCurrencies"
var CurrencyConfig = map[string]*ChainConfig{}

// This parses the viper config structure using ChainConfigDecoder to populate
// CurrencyConfig with ChainConfig structures
func (s *Service) SetupCurrencies() {
	for _, rawConfig := range s.config.GetStringMap("Currencies") {
		var config ChainConfigDecoder
		err := mapstructure.Decode(rawConfig, &config)
		if err != nil {
			panic(err)
		}
		log.Debug("Decoded currency config", "config", config, "rawConfig", rawConfig)

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
			log.Crit("Error decoding SubsidyAddress",
				"address", config.SubsidyAddress,
				"err", err,
				"currency", config.Code)
			os.Exit(1)
		}

		fa, err := btcutil.DecodeAddress(config.FeeAddress, params)
		if err != nil {
			panic(err)
		}

		if config.BlockMatureConfirms == 0 {
			panic("You must specify a block confirmation number")
		}

		cc := &ChainConfig{
			Code:                 config.Code,
			BlockMatureConfirms:  config.BlockMatureConfirms,
			PayoutTransactionFee: config.PayoutTransactionFee,

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
