package service

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScrypt(t *testing.T) {
	// The genesis block of litecoin testnet
	headerHex := "010000000000000000000000000000000000000000000000000000000000000000000000d9ced4ed1130f7b7faad9be25323ffafa33232a17c3edf6cfd97bee6bafbdd97f60ba158f0ff0f1ee1790400"
	header, _ := hex.DecodeString(headerHex)
	hsh, _ := AlgoConfig["scrypt"].PoWHash(header)
	hshHex := hex.EncodeToString(hsh)
	assert.Equal(t, "64de605b080d5c80ef6bf8460faf954bffb170d64d6087c3b4c42502cc060000", hshHex)
}

func TestSha256d(t *testing.T) {
	// The genesis block header of bitcoin testnet
	headerHex := "0100000000000000000000000000000000000000000000000000000000000000000000003ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa4b1e5e4adae5494dffff001d1aa4ae18"
	header, _ := hex.DecodeString(headerHex)
	hsh, _ := AlgoConfig["sha256d"].PoWHash(header)
	hshHex := hex.EncodeToString(hsh)
	assert.Equal(t, "43497fd7f826957108f4a30fd9cec3aeba79972084e90ead01ea330900000000", hshHex)
}

func TestLyra2rev2(t *testing.T) {
	// The genesis block header of vertcoin testnet
	headerHex := "010000000000000000000000000000000000000000000000000000000000000000000000e72301fc49323ee151cf1048230f032ca589753ba7086222a5c023e3a08cf34af2b54a58f0ff0f1e53f60d00"
	header, _ := hex.DecodeString(headerHex)
	hsh, _ := AlgoConfig["lyra2rev2"].PoWHash(header)
	hshHex := hex.EncodeToString(hsh)
	assert.Equal(t, "34f429a69dd5798d133ed6effddf52ed1b503538f8ecd934827d565dcd010000", hshHex)
}
