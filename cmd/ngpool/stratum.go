package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/dustin/go-broadcast"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
	"github.com/seehuhn/sha256d"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"math/big"
	"sync"
	"time"
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

func (t *BlockTemplate) getTarget() (*big.Int, error) {
	bits, err := hex.DecodeString(t.Bits)
	if err != nil {
		return nil, err
	}
	bitsUint := binary.BigEndian.Uint32(bits)
	return blockchain.CompactToBig(bitsUint), nil
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

// A placeholder for the extranonce
var extraNonceMagic []byte = []byte{0xdb, 0xa9, 0xf8, 0x6a, 0xfc, 0xc7, 0x27, 0x59}
var defaultNet = &chaincfg.MainNetParams

func (b *BlockTemplate) createCoinbase(chainConfig *ChainConfig) ([]byte, []byte, error) {
	// Create the script to pay to the provided payment address.
	pkScript, err := txscript.PayToAddrScript(*chainConfig.BlockSubsidyAddress)
	if err != nil {
		return nil, nil, err
	}

	cbScript, err := txscript.NewScriptBuilder().AddInt64(int64(b.Height + 1)).
		AddData(extraNonceMagic).Script()
	if err != nil {
		return nil, nil, err
	}

	tx := wire.NewMsgTx(1)
	tx.AddTxIn(&wire.TxIn{
		// Coinbase transactions have no inputs, so previous outpoint is
		// zero hash and max index.
		PreviousOutPoint: *wire.NewOutPoint(&chainhash.Hash{}, wire.MaxPrevOutIndex),
		SignatureScript:  cbScript,
		Sequence:         wire.MaxTxInSequenceNum,
	})
	tx.AddTxOut(&wire.TxOut{
		Value:    b.CoinbaseValue,
		PkScript: pkScript,
	})

	buf := bytes.Buffer{}
	tx.Serialize(&buf)
	txRaw := buf.Bytes()
	parts := bytes.Split(txRaw, extraNonceMagic)
	if len(parts) != 2 {
		return nil, nil, errors.New("Magic value collision!")
	}

	return parts[0], parts[1], nil
}

type StratumServer struct {
	config          *viper.Viper
	getTemplateCast func(TemplateKey) broadcast.Broadcaster
	jobCast         broadcast.Broadcaster
}

func NewStratumServer(config *viper.Viper, getTemplateCast func(TemplateKey) broadcast.Broadcaster) *StratumServer {
	ss := &StratumServer{
		config:          config,
		getTemplateCast: getTemplateCast,
		jobCast:         broadcast.NewBroadcaster(10),
	}
	return ss
}

type Job struct {
	bits          []byte
	time          []byte
	version       []byte
	prevBlockHash []byte
	coinbase1     []byte
	coinbase2     []byte
	merkleBranch  [][]byte
	target        *big.Int
	transactions  [][]byte
}

func genJob(templates map[TemplateKey][]byte) (*Job, error) {
	for tmplKey, tmplRaw := range templates {
		if tmplKey.TemplateType != "getblocktemplate" {
			log.WithField("type", tmplKey.TemplateType).Error("Unrecognized template type")
			continue
		}

		var tmpl BlockTemplate
		err := json.Unmarshal(tmplRaw, &tmpl)
		if err != nil {
			return nil, errors.Wrapf(err, "Unable to deserialize template: %v", string(tmplRaw))
		}

		chainConfig := CurrencyConfig[tmplKey.Currency]
		coinbase1, coinbase2, err := tmpl.createCoinbase(chainConfig)
		if err != nil {
			return nil, errors.Wrap(err, "Unable to create coinbase")
		}
		target, err := tmpl.getTarget()
		if err != nil {
			return nil, errors.Wrap(err, "Error generating target")
		}

		encodedTime := make([]byte, 4)
		binary.LittleEndian.PutUint32(encodedTime[0:], uint32(tmpl.CurTime))

		encodedVersion := make([]byte, 4)
		binary.LittleEndian.PutUint32(encodedVersion[0:], uint32(tmpl.Version))

		encodedPrevBlockHash, err := hex.DecodeString(tmpl.PreviousBlockhash)
		if err != nil {
			return nil, errors.Wrap(err, "Invalid PreviousBlockhash")
		}
		reverseBytes(encodedPrevBlockHash)

		encodedBits, err := hex.DecodeString(tmpl.Bits)
		if err != nil {
			return nil, errors.Wrap(err, "Invalid bits")
		}

		transactions := [][]byte{}
		for _, tx := range tmpl.Transactions {
			decoded, err := hex.DecodeString(tx.Data)
			if err != nil {
				return nil, errors.Wrap(err, "Invalid data from txn")
			}
			transactions = append(transactions, decoded)
		}

		job := &Job{
			transactions:  transactions,
			bits:          encodedBits,
			time:          encodedTime,
			version:       encodedVersion,
			prevBlockHash: encodedPrevBlockHash,
			coinbase1:     coinbase1,
			coinbase2:     coinbase2,
			target:        target,
			merkleBranch:  tmpl.merkleBranch(),
		}
		return job, nil
	}
	return nil, errors.New("Not sufficient templates to build job")
}

func (s *StratumServer) Miner() {
	listener := make(chan interface{})
	s.jobCast.Register(listener)
	jobLock := sync.Mutex{}
	var job *Job

	// Watch for new jobs for us
	go func() {
		for {
			jobOrig := <-listener
			newJob, ok := jobOrig.(*Job)
			if job == nil || !ok {
				log.WithField("job", jobOrig).Warn("Bad job from broadcast")
			}
			jobLock.Lock()
			job = newJob
			jobLock.Unlock()
		}
	}()

	go func() {
		var (
			blockHash = &big.Int{}
			rootHash  []byte
			hasher    = sha256d.New()
		)

		var i uint32 = 0
		for {
			if 1%1000 == 0 {
				log.Info("1khash done")
			}
			if job == nil {
				time.Sleep(time.Second * 1)
				continue
			}
			jobLock.Lock()
			buf := bytes.Buffer{}
			buf.Write(job.version)
			buf.Write(job.prevBlockHash)

			coinbase := bytes.Buffer{}
			coinbase.Write(job.coinbase1)
			coinbase.Write(extraNonceMagic)
			coinbase.Write(job.coinbase2)

			// Hash the coinbase
			hasher.Write(coinbase.Bytes())
			rootHash = hasher.Sum(nil)
			hasher.Reset()

			for _, branch := range job.merkleBranch {
				hasher.Write(rootHash)
				hasher.Write(branch)
				rootHash = hasher.Sum(nil)
				hasher.Reset()
			}

			buf.Write(rootHash)
			buf.Write(job.bits)
			binary.Write(&buf, binary.BigEndian, i)

			hasher.Write(buf.Bytes())
			blockHash.SetBytes(hasher.Sum(nil))
			hasher.Reset()
			if blockHash.Cmp(job.target) == -1 {
				for _, t := range job.transactions {
					buf.Write(t)
				}
				log.Infof("Found a block! \n%x", buf.Bytes())
				return
			}
			jobLock.Unlock()
			i += 1
		}
	}()
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
				job, err := genJob(latestTemp)
				log.Info("Generated new job, pushing...")
				if err != nil {
					log.WithError(err).Error("Error generating job")
					continue
				}
				s.jobCast.Submit(job)
				latestTempMtx.Unlock()
			}
		}()
	}

	go s.Miner()
}
