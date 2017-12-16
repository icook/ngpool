package service

import (
	"golang.org/x/crypto/scrypt"
	"math/big"
)

type HashFunc func(input []byte) ([]byte, error)

func scryptHash(input []byte) ([]byte, error) {
	return scrypt.Key(input, input, 1024, 1, 1, 32)
}

type Algo struct {
	PoWHash HashFunc
	Diff1   *big.Float
}

func (a *Algo) Diff1SharesForDiff(blockTarget float64) (float64, big.Accuracy) {
	blockTargetBig := big.NewFloat(blockTarget)
	diff1 := new(big.Float).Set(a.Diff1)
	return diff1.Quo(diff1, blockTargetBig).Float64()
}

func NewAlgoConfig(name string, diff1Hex string, powFunc HashFunc) *Algo {
	diff1 := big.Float{}
	_, _, err := diff1.Parse(diff1Hex, 16)
	if err != nil {
		panic(err)
	}

	ac := &Algo{
		Diff1:   &diff1,
		PoWHash: powFunc,
	}
	AlgoConfig[name] = ac
	return ac
}

var AlgoConfig = map[string]*Algo{}

func init() {
	NewAlgoConfig(
		"scrypt",
		"0000ffff00000000000000000000000000000000000000000000000000000000",
		scryptHash)
}
