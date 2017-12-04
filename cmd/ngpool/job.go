package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/pkg/errors"
	"github.com/seehuhn/sha256d"
	log "github.com/sirupsen/logrus"
	"math/big"
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

func (j *Job) getCoinbase(extraNonce []byte) []byte {
	coinbase := bytes.Buffer{}
	coinbase.Write(j.coinbase1)
	coinbase.Write(extraNonce)
	coinbase.Write(j.coinbase2)
	return coinbase.Bytes()
}

func (j *Job) getBlockHeader(nonce []byte, extraNonce []byte, coinbase []byte) []byte {
	var hasher = sha256d.New()
	buf := bytes.Buffer{}
	buf.Write(j.version)
	buf.Write(j.prevBlockHash)

	// Hash the coinbase, then walk down the merkle branch to get merkle root
	hasher.Write(coinbase)
	rootHash := hasher.Sum(nil)
	hasher.Reset()

	for _, branch := range j.merkleBranch {
		hasher.Write(rootHash)
		hasher.Write(branch)
		rootHash = hasher.Sum(nil)
		hasher.Reset()
	}

	buf.Write(rootHash)
	buf.Write(j.time)
	buf.Write(j.bits)
	buf.Write(nonce)

	return buf.Bytes()
}

func (j *Job) getBlock(header []byte, coinbase []byte) []byte {
	block := bytes.Buffer{}
	block.Write(header)
	wire.WriteVarInt(&block, 0, uint64(len(j.transactions)+1))
	block.Write(coinbase)

	for _, t := range j.transactions {
		block.Write(t)
	}
	return block.Bytes()
}

func NewJobFromTemplate(tmpl *BlockTemplate, config *ChainConfig) (*Job, error) {
	coinbase1, coinbase2, err := tmpl.createCoinbase(config)
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
	reverseBytes(encodedBits)

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
