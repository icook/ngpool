package service

import (
	"encoding/json"
	"golang.org/x/crypto/scrypt"
	"math/big"
)

type HashFunc func(input []byte) ([]byte, error)

func scryptHash(input []byte) ([]byte, error) {
	return scrypt.Key(input, input, 1024, 1, 1, 32)
}

type Algo struct {
	Name       string
	PoWHash    HashFunc
	ShareDiff1 *big.Float
	NetDiff1   float64
}

func (u *Algo) MarshalJSON() ([]byte, error) {
	sharediff1Float, _ := u.ShareDiff1.Float64()
	return json.Marshal(&struct {
		Name       string   `json:"name"`
		PoWHash    HashFunc `json:"-"`
		ShareDiff1 float64  `json:"share_diff1"`
		NetDiff1   float64  `json:"net_diff1"`
	}{
		Name:       u.Name,
		PoWHash:    u.PoWHash,
		ShareDiff1: sharediff1Float,
		NetDiff1:   u.NetDiff1,
	})
}

func (a *Algo) Diff1SharesForTarget(blockTarget float64) (float64, big.Accuracy) {
	blockTargetBig := big.NewFloat(blockTarget)
	diff1 := new(big.Float).Set(a.ShareDiff1)
	return diff1.Quo(diff1, blockTargetBig).Float64()
}

func NewAlgoConfig(name string, diff1Hex string, powFunc HashFunc) *Algo {
	diff1 := big.Float{}
	_, _, err := diff1.Parse(diff1Hex, 16)
	if err != nil {
		panic(err)
	}

	shareDiff1, _ := diff1.Float64()

	ac := &Algo{
		Name:       name,
		ShareDiff1: &diff1,
		NetDiff1:   shareDiff1 / (0xFFFF - 1),
		PoWHash:    powFunc,
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
