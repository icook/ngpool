package main

import (
	"bytes"
	"fmt"
	"github.com/jmoiron/sqlx"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"
)

type TestHarness struct {
	adminClient *sqlx.DB
	T           *testing.T
	NgWebAPI
}

func NewHarness(t *testing.T, configs ...string) *TestHarness {
	qt := NewNgWebAPI()

	config := bytes.Buffer{}
	for _, p := range configs {
		config.WriteString(p)
	}
	qt.config.MergeConfig(&config)
	qt.ConnectDB()

	// Create a new user to run this test under. We do this because we set
	// "search_path" to pick which schema all the SQL in qtrade acts on. Since
	// this is a per-user setting, we must make a user
	rand.Seed(time.Now().UnixNano())
	testKey := strconv.FormatInt(rand.Int63(), 10)
	username := "ngweb" + testKey
	qt.db.MustExec(
		fmt.Sprintf("CREATE ROLE %s WITH LOGIN SUPERUSER PASSWORD 'knight'", username))

	// Swap the database connection to our new user
	qt.config.Set("DbUser", username)
	qt.config.Set("DbPassword", "knight")
	adminClient := qt.db
	qt.ConnectDB()

	qt.ParseConfig()

	h := TestHarness{
		T:           t,
		NgWebAPI:    *qt,
		adminClient: adminClient,
	}
	return &h
}

func TestBasic(t *testing.T) {
	config := `
ShareChains:
    "LTC_T":
        name: "LTC_T"
        fee: 0.01
        payoutmethod: "pplns"
Currencies:
    "LTC_T":
        code: "LTC_T"
        subsidyAddress: "mucHkBoHAF8DTQWFuwQXHiewqi3ZBDNNWh"
        feeAddress: "mucHkBoHAF8DTQWFuwQXHiewqi3ZBDNNWh"
        powalgorithm: "scrypt"
        pubkeyaddrid: "6f"
        privkeyaddrid: "ef"
        netmagic: 0xfdd2c8f1
	`
	ng := NewHarness(config)
	ng.ParseConfig()
	ng.GenerateCredits()
	t.FailNow()
}
