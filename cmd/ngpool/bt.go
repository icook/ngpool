package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math/big"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	log "github.com/inconshreveable/log15"
	"github.com/pkg/errors"
	"github.com/seehuhn/sha256d"
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
	Extras            struct {
		ChainID int
	}
}

func (t *BlockTemplate) getTarget() (*big.Int, error) {
	bits, err := hex.DecodeString(t.Bits)
	if err != nil {
		return nil, err
	}
	bitsUint := binary.BigEndian.Uint32(bits)
	return blockchain.CompactToBig(bitsUint), nil
}

func (b *BlockTemplate) merkleBranch() [][]byte {
	branch := [][]byte{}
	// Create a list of txn hashes with a placeholder for the coinbase that is
	// =nil={0}
	hashes := [][]byte{{}}
	for _, txn := range b.Transactions {
		txID, err := hex.DecodeString(txn.getTxID())
		if err != nil {
			log.Warn("Invalid txid from gbt", "err", err)
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

func (b *BlockTemplate) createCoinbaseSplit(chainConfig *ChainConfig, extra []byte) ([]byte, []byte, error) {
	newExtra := append(extra, extraNonceMagic...)
	txRaw, err := b.createCoinbase(chainConfig, newExtra)
	if err != nil {
		return nil, nil, err
	}
	parts := bytes.Split(txRaw, extraNonceMagic)
	if len(parts) != 2 {
		return nil, nil, errors.New("Magic value collision!")
	}
	return parts[0], parts[1], nil
}

func (b *BlockTemplate) createCoinbase(chainConfig *ChainConfig, extra []byte) ([]byte, error) {
	// Create the script to pay to the provided payment address.
	pkScript, err := txscript.PayToAddrScript(*chainConfig.BlockSubsidyAddress)
	if err != nil {
		return nil, err
	}

	cbScript, err := txscript.NewScriptBuilder().AddInt64(int64(b.Height)).
		AddData(extra).Script()
	if err != nil {
		return nil, err
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

	return buf.Bytes(), nil
}

func (b *BlockTemplate) merkleRoot(coinbase []byte) []byte {
	var hasher = sha256d.New()
	hasher.Write(coinbase)
	coinbaseHash := hasher.Sum(nil)
	hashes := [][]byte{coinbaseHash}
	for _, txn := range b.Transactions {
		txID, err := hex.DecodeString(txn.getTxID())
		if err != nil {
			log.Warn("Invalid txid from gbt", "err", err)
		}
		reverseBytes(txID)
		hashes = append(hashes, txID)
	}
	return merkleRoot(hashes)
}

func auxMerkleBranch(merkleBase [][]byte, followHash []byte) ([][]byte, uint32) {
	branch := [][]byte{}
	hashes := merkleBase
	for i := 0; i < len(hashes); i = i + 1 {
		if hashes[i] == nil {
			hashes[i] = []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		}
	}
	follow := followHash[:]
	var level uint32 = 0
	var bitMask uint32
	for {
		if len(hashes) <= 1 {
			break
		}
		newHashes := [][]byte{}
		for i := 0; i < len(hashes); i = i + 2 {
			hsh := sha256d.New()
			if i+1 >= len(hashes) {
				hsh.Write(append(hashes[i], hashes[i]...))
				newHashes = append(newHashes, hsh.Sum(nil))
				// Hash the pair together to form next higher level in tree
			} else {
				hsh.Write(append(hashes[i], hashes[i+1]...))
				newHashes = append(newHashes, hsh.Sum(nil))
			}

			// If we're hashing the relevant block hash
			if bytes.Compare(hashes[i], follow) == 0 {
				branch = append(branch, hashes[i+1])
				follow = hsh.Sum(nil)
				// bitmask already set to 0
			} else if bytes.Compare(hashes[i+1], follow) == 0 {
				bitMask |= (1 << level)
				branch = append(branch, hashes[i+1])
				follow = hsh.Sum(nil)
			}
		}
		// Operate next iteration of loop on the newly generated level of the tree
		hashes = newHashes
	}
	return branch, bitMask
}

func merkleRoot(hashes [][]byte) []byte {
	for {
		if len(hashes) <= 1 {
			break
		}
		newHashes := [][]byte{}
		for i := 0; i < len(hashes); i = i + 2 {
			if i+1 >= len(hashes) {
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
	return hashes[0]
}

func reverseBytes(s []byte) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
