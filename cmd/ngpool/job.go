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
	log "github.com/inconshreveable/log15"
	"github.com/pkg/errors"
	"github.com/seehuhn/sha256d"
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

func reverseBytes(s []byte) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func auxMerkleBranch(merkleBase [][]byte, followHash []byte) ([][]byte, uint32) {
	branch := [][]byte{}
	hashes := [][]byte{}
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
var defaultNet = &chaincfg.MainNetParams

func (b *BlockTemplate) createCoinbaseSplit(chainConfig *ChainConfig, extra []byte) ([]byte, []byte, error) {
	txRaw, err := b.createCoinbase(chainConfig, extra)
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

	cbScript, err := txscript.NewScriptBuilder().AddInt64(int64(b.Height + 1)).
		AddData(extraNonceMagic).AddData(extra).Script()
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

type MainChainJob struct {
	currencyConfig *ChainConfig
	bits           []byte
	time           []byte
	version        []byte
	prevBlockHash  []byte
	coinbase1      []byte
	coinbase2      []byte
	merkleBranch   [][]byte

	target       *big.Int
	transactions [][]byte
}
type AuxChainJob struct {
	currencyConfig         *ChainConfig
	headerHash             *chainhash.Hash
	blockHeader            []byte
	chainID                int
	blockchainMerkleBranch [][]byte
	blockchainMerkleMask   uint32
	transactions           [][]byte
	coinbase               []byte
	target                 *big.Int
}

type Job struct {
	MainChainJob
	auxChains []*AuxChainJob
}

func NewJobFromTemplates(templates map[TemplateKey][]byte) (*Job, error) {
	var (
		mainJobSet      bool
		mainJobTemplate *BlockTemplate
	)
	job := Job{}
	for tmplKey, tmplRaw := range templates {
		var tmpl BlockTemplate
		err := json.Unmarshal(tmplRaw, &tmpl)
		if err != nil {
			return nil, errors.Wrapf(err, "Unable to deserialize template: %v", string(tmplRaw))
		}
		chainConfig, ok := CurrencyConfig[tmplKey.Currency]
		if !ok {
			return nil, errors.Errorf("No currency config for %s", tmplKey.Currency)
		}

		switch tmplKey.TemplateType {
		case "getblocktemplate_aux":
			auxChainJob, err := NewAuxChainJob(&tmpl, chainConfig)
			if err != nil {
				return nil, err
			}
			job.auxChains = append(job.auxChains, auxChainJob)
		case "getblocktemplate":
			if mainJobSet {
				return nil, errors.Errorf("You can only have one base currency template")
			}
			mainJobSet = true
			mainChainJob, err := NewMainChainJob(&tmpl, chainConfig)
			if err != nil {
				return nil, err
			}
			job.MainChainJob = *mainChainJob
			mainJobTemplate = &tmpl
		default:
			return nil, errors.Errorf("Unrecognized TemplateType %s", tmplKey.TemplateType)
		}
	}
	if !mainJobSet {
		return nil, errors.New("Must have a main chain template")
	}

	// Build the merge mining merkle tree
	var merkleSize = 1
	var merkleBase [][]byte
MerkleLoop:
	for {
		// A candidate for the size of our blockchain merkle tree. If it fails
		// we iterate
		merkleBase = make([][]byte, merkleSize)
		for _, mj := range job.auxChains {
			var slot int = 0
			slot = slot*1103515245 + 12345
			slot += mj.chainID
			slot = slot*1103515245 + 12345
			slotNum := slot % merkleSize
			if merkleBase[slotNum] != nil {
				merkleSize += 1
				continue MerkleLoop
			}
		}
		break
	}

	for _, mj := range job.auxChains {
		branch, mask := auxMerkleBranch(merkleBase, []byte(mj.headerHash.String()))
		mj.blockchainMerkleBranch = branch
		mj.blockchainMerkleMask = mask
	}

	mmCoinbase := bytes.Buffer{}
	mmCoinbase.Write([]byte{0xfa, 0xbe, 'm', 'm'})
	mmCoinbase.Write(merkleRoot(merkleBase))
	encodedMerkleSize := make([]byte, 4)
	binary.LittleEndian.PutUint32(encodedMerkleSize[0:], uint32(merkleSize))
	mmCoinbase.Write(encodedMerkleSize)
	mmCoinbase.Write([]byte{0, 0, 0, 0})

	coinbase1, coinbase2, err := mainJobTemplate.createCoinbaseSplit(job.currencyConfig, mmCoinbase.Bytes())
	if err != nil {
		return nil, errors.Wrap(err, "Unable to create coinbase")
	}
	job.coinbase1 = coinbase1
	job.coinbase2 = coinbase2
	return &job, nil
}

func (j *Job) CheckSolves(nonce []byte, extraNonce []byte, shareTarget *big.Int) (map[string][]byte, bool, error) {
	var ret = map[string][]byte{}
	var validShare = false

	coinbase := bytes.Buffer{}
	coinbase.Write(j.coinbase1)
	coinbase.Write(extraNonce)
	coinbase.Write(j.coinbase2)
	header := j.GetBlockHeader(nonce, extraNonce)
	headerHsh, err := j.currencyConfig.PoWHash(header)
	if err != nil {
		return nil, false, err
	}
	hashObj, err := chainhash.NewHash(headerHsh)
	if err != nil {
		return nil, false, err
	}
	bigHsh := blockchain.HashToBig(hashObj)
	if shareTarget != nil && bigHsh.Cmp(shareTarget) <= 0 {
		validShare = true
	}

	if bigHsh.Cmp(j.target) <= 0 {
		ret[j.currencyConfig.Code] = j.GetBlock(header, coinbase.Bytes())
	}

	for _, mj := range j.auxChains {
		if bigHsh.Cmp(mj.target) <= 0 {
			ret[mj.currencyConfig.Code] = mj.GetBlock(
				coinbase.Bytes(), headerHsh, j.merkleBranch, header)
		}
	}
	return ret, validShare, nil
}

func (j *MainChainJob) GetBlockHeader(nonce []byte, coinbase []byte) []byte {
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

func (j *MainChainJob) GetBlock(header []byte, coinbase []byte) []byte {
	block := bytes.Buffer{}
	block.Write(header)
	wire.WriteVarInt(&block, 0, uint64(len(j.transactions)+1))
	block.Write(coinbase)

	for _, t := range j.transactions {
		block.Write(t)
	}
	return block.Bytes()
}

func (j *AuxChainJob) GetBlock(coinbase []byte, parentHash []byte, coinbaseBranch [][]byte, parentHeader []byte) []byte {
	block := bytes.Buffer{}
	block.Write(j.blockHeader)
	block.Write(j.coinbase)
	block.Write(parentHash)
	// Coinbase merkle branch
	wire.WriteVarInt(&block, 0, uint64(len(coinbaseBranch)))
	for _, branch := range coinbaseBranch {
		block.Write(branch)
	}
	// Coinbase branch mask is always all zeros (right, right, right...)
	block.Write([]byte{0, 0, 0, 0})

	// Blockchain merkle branch
	wire.WriteVarInt(&block, 0, uint64(len(j.blockchainMerkleBranch)))
	for _, branch := range j.blockchainMerkleBranch {
		block.Write(branch)
	}
	// Coinbase branch mask is always all zeros (right, right, right...)
	encodedMask := make([]byte, 4)
	binary.LittleEndian.PutUint32(encodedMask, j.blockchainMerkleMask)
	block.Write(encodedMask)

	block.Write(parentHeader)
	wire.WriteVarInt(&block, 0, uint64(len(j.transactions)+1))
	block.Write(j.coinbase)

	for _, t := range j.transactions {
		block.Write(t)
	}
	return block.Bytes()
}

func NewAuxChainJob(template *BlockTemplate, config *ChainConfig) (*AuxChainJob, error) {
	target, err := template.getTarget()
	if err != nil {
		return nil, errors.Wrap(err, "Error generating target")
	}

	blkHeader := bytes.Buffer{}
	encodedVersion := make([]byte, 4)
	version := uint32(template.Version)
	version |= (1 << 8)
	binary.LittleEndian.PutUint32(encodedVersion[0:], version)
	blkHeader.Write(encodedVersion)

	encodedPrevBlockHash, err := hex.DecodeString(template.PreviousBlockhash)
	if err != nil {
		return nil, errors.Wrap(err, "Invalid PreviousBlockhash")
	}
	reverseBytes(encodedPrevBlockHash)
	blkHeader.Write(encodedPrevBlockHash)

	// Hash the coinbase, then walk down the merkle branch to get merkle root
	coinbase, err := template.createCoinbase(config, []byte{})
	if err != nil {
		return nil, err
	}
	blkHeader.Write(template.merkleRoot(coinbase))

	encodedTime := make([]byte, 4)
	binary.LittleEndian.PutUint32(encodedTime[0:], uint32(template.CurTime))
	blkHeader.Write(encodedTime)

	encodedBits, err := hex.DecodeString(template.Bits)
	if err != nil {
		return nil, errors.Wrap(err, "Invalid bits")
	}
	reverseBytes(encodedBits)
	blkHeader.Write(encodedBits)
	blkHeader.Write([]byte{0, 0, 0, 0})

	var hasher = sha256d.New()
	hasher.Write(blkHeader.Bytes())
	hashObj, err := chainhash.NewHash(hasher.Sum(nil))
	if err != nil {
		return nil, err
	}

	transactions := [][]byte{}
	for _, tx := range template.Transactions {
		decoded, err := hex.DecodeString(tx.Data)
		if err != nil {
			return nil, errors.Wrap(err, "Invalid data from txn")
		}
		transactions = append(transactions, decoded)
	}

	acj := &AuxChainJob{
		currencyConfig: config,
		target:         target,
		coinbase:       coinbase,
		transactions:   transactions,
		chainID:        template.Extras.ChainID,
		headerHash:     hashObj,
		blockHeader:    blkHeader.Bytes(),
	}
	return acj, nil
}

func NewMainChainJob(tmpl *BlockTemplate, config *ChainConfig) (*MainChainJob, error) {
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

	job := &MainChainJob{
		currencyConfig: config,
		transactions:   transactions,
		bits:           encodedBits,
		time:           encodedTime,
		version:        encodedVersion,
		prevBlockHash:  encodedPrevBlockHash,
		target:         target,
		merkleBranch:   tmpl.merkleBranch(),
	}
	return job, nil
}
