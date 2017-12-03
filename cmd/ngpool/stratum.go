package main

import (
	"encoding/hex"
	"encoding/json"
	//"github.com/davecgh/go-spew/spew"
	"github.com/dustin/go-broadcast"
	"github.com/mitchellh/mapstructure"
	"github.com/seehuhn/sha256d"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"math/big"
	"sync"
)

type GBTTransaction struct {
	Data    string
	TxID    string
	Hash    string
	Depends []int64
	Fee     int64
	SigOps  int64
	Weight  int64
}

func (t *GBTTransaction) getTxID() string {
	// Pre-segwit hash == txid, but post segwit hash includes witness data,
	// while txid does not. Old clients don't populate txid tho, and we must
	// instead use hash
	if t.TxID == "" {
		return t.Hash
	}
	return t.TxID
}

type BlockTemplate struct {
	Capabilities      []string
	Version           int64
	Rules             []string
	Vbavailable       map[string]int64
	Vbrequired        int64
	PreviousBlockhash string
	Transactions      []GBTTransaction
	CoinbaseAux       map[string]string
	CoinbaseValue     int64
	CoinbaseTxn       interface{}
	Target            string
	MinTime           int64
	Mutable           []string
	NonceRange        string
	SigOpLimit        int64
	SizeLimit         int64
	WeightLimit       int64
	CurTime           int64
	Bits              string
	Height            int64
}

func (t *BlockTemplate) getTarget() big.Int {
	return big.Int{}
}

func reverseBytes(s []byte) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func (b *BlockTemplate) merkleBranch() [][]byte {
	branch := [][]byte{}
	// Create a list of txn hashes with a placeholder for the coinbase that is
	// =nil={0}
	hashes := [][]byte{{}}
	for _, txn := range b.Transactions {
		txID, err := hex.DecodeString(txn.getTxID())
		if err != nil {
			log.WithError(err).Warn("Invalid txid from gbt")
		}
		reverseBytes(txID)
		hashes = append(hashes, txID)
	}
	for {
		if len(hashes) <= 1 {
			break
		}
		branch = append(branch, hashes[1])
		newHashes := [][]byte{}
		for i := 0; i < len(hashes); i = i + 2 {
			// We're generating the next higher level of the tree
			if len(hashes[i]) == 0 { // if we encounter the placeholder, keep it
				newHashes = append(newHashes, hashes[i])
				// If we don't have a pair of hashes (odd list size) the rule is hash with itself
			} else if i+1 >= len(hashes) {
				hsh := sha256d.New()
				hsh.Write(append(hashes[i], hashes[i]...))
				newHashes = append(newHashes, hsh.Sum(nil))
				// Hash the pair together to form next higher level in tree
			} else {
				hsh := sha256d.New()
				hsh.Write(append(hashes[i], hashes[i+1]...))
				newHashes = append(newHashes, hsh.Sum(nil))
			}
		}
		// Operate next iteration of loop on the newly generated level of the tree
		hashes = newHashes
	}
	return branch
}

type StratumServer struct {
	config          *viper.Viper
	getTemplateCast func(TemplateKey) broadcast.Broadcaster
}

func NewStratumServer(config *viper.Viper, getTemplateCast func(TemplateKey) broadcast.Broadcaster) *StratumServer {
	ss := &StratumServer{
		config:          config,
		getTemplateCast: getTemplateCast,
	}
	return ss
}

func genJob(templates map[TemplateKey][]byte) {
	for tmplKey, tmplRaw := range templates {
		if tmplKey.TemplateType != "getblocktemplate" {
			log.WithField("type", tmplKey.TemplateType).Error("Unrecognized template type")
			continue
		}
		var tmpl BlockTemplate
		err := json.Unmarshal(tmplRaw, &tmpl)
		if err != nil {
			log.WithError(err).WithField("template", string(tmplRaw)).Error("Unable to deserialize block template")
			continue
		}
		tmpl.merkleBranch()
	}
}

func (s *StratumServer) Start() {
	latestTemp := map[TemplateKey][]byte{}
	latestTempMtx := sync.Mutex{}

	vals := s.config.Get("Currencies")
	for _, val := range vals.([]interface{}) {
		var tmplKey TemplateKey
		err := mapstructure.Decode(val, &tmplKey)
		if err != nil {
			log.WithError(err).Error("Invalid configuration, currencies of improper format")
			return
		}
		go func() {
			log.Infof("Registering listener for %+v", tmplKey)
			listener := make(chan interface{})
			broadcast := s.getTemplateCast(tmplKey)
			broadcast.Register(listener)
			defer func() {
				log.Debug("Closing template listener channel")
				broadcast.Unregister(listener)
				close(listener)
			}()
			for {
				newTemplate := <-listener
				template, ok := newTemplate.([]byte)
				if !ok {
					log.Errorf("Got invalid type from template listener: %#v", newTemplate)
					continue
				}
				latestTempMtx.Lock()
				latestTemp[tmplKey] = template
				genJob(latestTemp)
				latestTempMtx.Unlock()
			}
		}()
	}
}
