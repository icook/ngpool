package service

import (
	log "github.com/inconshreveable/log15"
	"github.com/mitchellh/mapstructure"
)

type ShareChainConfig struct {
	Name         string
	PayoutMethod string
	Fee          float64
}

var ShareChain = map[string]*ShareChainConfig{}

func (s *Service) SetupShareChains() {
	// TODO: Chain to array of maps, makes more sense
	for _, rawConfig := range s.config.GetStringMap("ShareChains") {
		var chain ShareChainConfig
		err := mapstructure.Decode(rawConfig, &chain)
		if err != nil {
			panic(err)
		}
		log.Debug("Decoded share chain config", "chain", chain, "rawConfig", rawConfig)
		// TODO: Ensure supported PayoutMethod to avoid misconfiguration
		ShareChain[chain.Name] = &chain
	}
}
