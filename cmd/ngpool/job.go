package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/big"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/pkg/errors"
	"github.com/seehuhn/sha256d"
)

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
	var merkleNonce uint32 = 0
MerkleLoop:
	for {
		// A candidate for the size of our blockchain merkle tree. If it fails
		// we iterate
		merkleBase = make([][]byte, merkleSize)
		for _, mj := range job.auxChains {
			var slot uint32 = merkleNonce
			slot = slot*1103515245 + 12345
			slot += uint32(mj.chainID)
			slot = slot*1103515245 + 12345
			slotNum := slot % uint32(merkleSize)
			if merkleBase[slotNum] != nil {
				merkleSize *= 2
				continue MerkleLoop
			}
			merkleBase[slotNum] = mj.headerHash.CloneBytes()
		}
		break
	}

	for _, mj := range job.auxChains {
		branch, mask := auxMerkleBranch(merkleBase, mj.headerHash.CloneBytes())
		mj.blockchainMerkleBranch = branch
		mj.blockchainMerkleMask = mask
	}

	mmCoinbase := bytes.Buffer{}
	if len(job.auxChains) > 0 {
		mmCoinbase.Write([]byte{0xfa, 0xbe, 'm', 'm'})
		if len(job.auxChains) > 1 {
			merkleRoot := merkleRoot(merkleBase)
			reverseBytes(merkleRoot)
			mmCoinbase.Write(merkleRoot)
		} else {
			mj := job.auxChains[0]
			merkleRoot := mj.headerHash.CloneBytes()
			reverseBytes(merkleRoot)
			mmCoinbase.Write(merkleRoot)
		}
		// Merkle size
		encodedMerkleSize := make([]byte, 4)
		binary.LittleEndian.PutUint32(encodedMerkleSize[0:], uint32(merkleSize))
		mmCoinbase.Write(encodedMerkleSize)
		// Nonce
		encodedNonce := make([]byte, 4)
		binary.LittleEndian.PutUint32(encodedNonce, uint32(merkleNonce))
		mmCoinbase.Write(encodedNonce)
	}

	coinbase1, coinbase2, err := mainJobTemplate.createCoinbaseSplit(job.currencyConfig, mmCoinbase.Bytes())
	if err != nil {
		return nil, errors.Wrap(err, "Unable to create coinbase")
	}
	job.coinbase1 = coinbase1
	job.coinbase2 = coinbase2
	return &job, nil
}

func (j *Job) GetStratumParams() ([]interface{}, error) {
	var mb = []string{}
	for _, b := range j.merkleBranch {
		mb = append(mb, hex.EncodeToString(b))
	}
	return []interface{}{
		hex.EncodeToString(j.prevBlockHash),
		hex.EncodeToString(j.coinbase1),
		hex.EncodeToString(j.coinbase2),
		mb,
		hex.EncodeToString(j.version),
		hex.EncodeToString(j.bits),
		hex.EncodeToString(j.time),
		j.cleanJobs,
	}, nil
}

func (j *Job) CheckSolves(nonce []byte, extraNonce []byte, shareTarget *big.Int) (map[string][]byte, bool, error) {
	var ret = map[string][]byte{}
	var validShare = false

	coinbase := bytes.Buffer{}
	coinbase.Write(j.coinbase1)
	coinbase.Write(extraNonce)
	coinbase.Write(j.coinbase2)
	header := j.GetBlockHeader(nonce, coinbase.Bytes())
	headerHsh, err := j.currencyConfig.PoWHash(header)
	if err != nil {
		return nil, false, err
	}
	hashObj, err := chainhash.NewHash(headerHsh)
	if err != nil {
		return nil, false, err
	}
	bigHsh := blockchain.HashToBig(hashObj)
	// Share targets are in opposite endian of block targets (i think..), so
	// the comparison direction is opposite as well. Here we check if hash >
	// target, but below we check if hash < network_target
	if shareTarget != nil && bigHsh.Cmp(shareTarget) >= 0 {
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

	cleanJobs bool
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
		cleanJobs:      true, // TODO: change me
	}
	return job, nil
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

func NewAuxChainJob(template *BlockTemplate, config *ChainConfig) (*AuxChainJob, error) {
	target, err := template.getTarget()
	if err != nil {
		return nil, errors.Wrap(err, "Error generating target")
	}

	blkHeader := bytes.Buffer{}
	encodedVersion := make([]byte, 4)
	version := uint32(template.Version)
	// Set flag for an AuxPoW block
	version |= (1 << 8)
	version |= (uint32(template.Extras.ChainID) << 16)
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
	merkleRoot := template.merkleRoot(coinbase)
	blkHeader.Write(merkleRoot)

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

	if template.Extras.ChainID == 0 {
		return nil, errors.New("Null chainid")
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
func (j *AuxChainJob) GetBlock(coinbase []byte, parentHash []byte, coinbaseBranch [][]byte, parentHeader []byte) []byte {
	block := bytes.Buffer{}
	block.Write(j.blockHeader)
	block.Write(coinbase)
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
