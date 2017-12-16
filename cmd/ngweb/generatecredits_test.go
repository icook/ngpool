package main

import (
	"bytes"
	"fmt"
	"github.com/jmoiron/sqlx"
	"math/rand"
	"strconv"
	"testing"
	"time"
)

type TestHarness struct {
	adminClient *sqlx.DB
	T           *testing.T
	dbUsername  string
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
	username := "ngpool" + testKey
	qt.db.MustExec(
		fmt.Sprintf("CREATE ROLE %s WITH LOGIN SUPERUSER PASSWORD 'knight'", username))
	qt.db.MustExec(
		fmt.Sprintf("CREATE SCHEMA %s AUTHORIZATION %s", username, username))
	qt.db.MustExec(
		fmt.Sprintf("ALTER ROLE %s SET search_path TO %s", username, username))

	// Swap the database connection to our new user
	qt.config.Set("DbConnectionString",
		fmt.Sprintf("user=%s dbname=ngpool sslmode=disable password=knight", username))
	adminClient := qt.db
	qt.ConnectDB()
	qt.LoadFixtures("tables")
	qt.ParseConfig()

	h := TestHarness{
		T:           t,
		NgWebAPI:    *qt,
		adminClient: adminClient,
		dbUsername:  username,
	}
	return &h
}

func (h *TestHarness) Cleanup() {
	cmds := []string{
		fmt.Sprintf("DROP SCHEMA %s CASCADE", h.dbUsername),
		fmt.Sprintf("DROP ROLE %s", h.dbUsername),
	}
	for _, cmd := range cmds {
		h.adminClient.MustExec(cmd)
	}
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
	ng := NewHarness(t, config)
	defer ng.Cleanup()
	ng.LoadFixtures("user", "simple_solve")
	ng.GenerateCredits()
	t.FailNow()
}
