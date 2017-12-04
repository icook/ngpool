package main

import (
	"github.com/stretchr/testify/assert"
	"math/big"
	"testing"
)

func TestTarget(t *testing.T) {
	tests := []struct {
		enc string
		out *big.Int
	}{
		{"01003456", big.NewInt(0)},
		{"05009234", big.NewInt(0x92340000)},
		{"02008000", big.NewInt(0x80)},
	}
	for _, test := range tests {
		tmpl := BlockTemplate{Bits: test.enc}
		tar, _ := tmpl.getTarget()
		assert.Equal(t, test.out, tar)
	}
}

func TestBranch(t *testing.T) {
	tmpl := BlockTemplate{
		Transactions: []GBTTransaction{
			GBTTransaction{Hash: "666dceaf1a6a90786651028248decd08435ed8d8486e304846998ecfe70a4f2e"},
			GBTTransaction{Hash: "c29acd7e8c2b2ac21b0a2b1eeef8afda9451ce611328f6c34286dea129dd8759"},
			GBTTransaction{Hash: "ac81ff8190271ea43b102074db5a908234855cbfd41133d3824d991b51c9f585"},
			GBTTransaction{Hash: "2a42a2dac7b934748f28f97a96f2c035566e5a5ef4e19449afcf561a1989da20"},
			GBTTransaction{Hash: "95375a84f3ced372b2b2f30d36323d11a42f4646fc0b716fa6c8f47e6ce73b34"},
		}}
	correct := [][]uint8{
		[]uint8{0x2e, 0x4f, 0xa, 0xe7, 0xcf, 0x8e, 0x99, 0x46, 0x48, 0x30,
			0x6e, 0x48, 0xd8, 0xd8, 0x5e, 0x43, 0x8, 0xcd, 0xde, 0x48, 0x82, 0x2,
			0x51, 0x66, 0x78, 0x90, 0x6a, 0x1a, 0xaf, 0xce, 0x6d, 0x66},
		[]uint8{0x6c, 0x6f, 0x73, 0x8, 0xf0, 0x8f, 0xdf, 0x16, 0xdb, 0x8d,
			0x23, 0xf4, 0x99, 0x2, 0xec, 0x98, 0xa, 0x91, 0xcf, 0xa5, 0x50, 0x1a,
			0x24, 0xe3, 0x2e, 0x40, 0x1a, 0x6b, 0xe4, 0xa3, 0xde, 0x63},
		[]uint8{0xe3, 0xfd, 0x33, 0x18, 0x4d, 0xf4, 0x15, 0x4f, 0x75, 0xab,
			0xb7, 0xe0, 0xe, 0x79, 0x63, 0x3b, 0x95, 0x7d, 0x24, 0x29, 0xf7, 0xb1,
			0x35, 0xa7, 0x45, 0xd3, 0xe3, 0xfd, 0x1f, 0x3a, 0xf, 0x86},
	}
	assert.Equal(t, correct, tmpl.merkleBranch())
}
