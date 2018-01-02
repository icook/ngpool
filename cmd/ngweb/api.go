package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/icook/btcd/rpcclient"
	log "github.com/inconshreveable/log15"
	"github.com/itsjamie/gin-cors"
	"github.com/jmoiron/sqlx"
	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
	"gopkg.in/go-playground/validator.v9"

	"github.com/icook/ngpool/pkg/common"
	"github.com/icook/ngpool/pkg/service"
)

var validate = validator.New()

type NgWebAPI struct {
	log log.Logger

	config  *viper.Viper
	db      *sqlx.DB
	engine  *gin.Engine
	service *service.Service

	currencyRPC    map[string]*rpcclient.Client
	currencyRPCMtx *sync.RWMutex

	coinservers    map[string]*service.ServiceStatus
	coinserversMtx *sync.RWMutex

	stratums       map[string]*service.ServiceStatus
	stratumClients map[string][]*common.StratumClientStatus
	stratumsMtx    *sync.RWMutex
}

func NewNgWebAPI() *NgWebAPI {
	var ngw = NgWebAPI{
		log: log.New(),

		currencyRPC:    map[string]*rpcclient.Client{},
		currencyRPCMtx: &sync.RWMutex{},

		coinservers:    map[string]*service.ServiceStatus{},
		coinserversMtx: &sync.RWMutex{},

		stratums:       map[string]*service.ServiceStatus{},
		stratumClients: map[string][]*common.StratumClientStatus{},
		stratumsMtx:    &sync.RWMutex{},
	}

	return &ngw
}

func (q *NgWebAPI) ParseConfig() {
	// Load our configuration info
	q.service = service.NewService("api",
		[]string{"http://127.0.0.1:2379", "http://127.0.0.1:4001"})
	config := q.service.LoadCommonConfig()

	config.SetDefault("LogLevel", "info")
	config.SetDefault("DbConnectionString",
		"user=ngpool dbname=ngpool sslmode=disable password=knight")
	q.config = config

	// TODO: Check for secure JWTSecret

	levelConfig := q.config.GetString("LogLevel")
	level, err := log.LvlFromString(levelConfig)
	if err != nil {
		log.Crit("Unable to parse log level", "configval", levelConfig, "err", err)
		os.Exit(1)
	}
	handler := log.CallerFileHandler(log.StreamHandler(os.Stdout, log.TerminalFormat()))
	handler = log.LvlFilterHandler(level, handler)
	q.log.SetHandler(handler)
	q.log.Info("Set log level", "level", level)
}

func (q *NgWebAPI) ConnectDB() {
	db, err := sqlx.Connect("postgres", q.config.GetString("DbConnectionString"))
	if err != nil {
		q.log.Crit("Failed connect db", "err", err)
		os.Exit(1)
	}
	q.db = db
}

func (q *NgWebAPI) SetupGin() {
	// Setup our database connection
	// Configure webserver
	r := gin.Default()
	r.Use(cors.Middleware(cors.Config{
		Origins:         "*",
		Methods:         "GET, PUT, POST, DELETE",
		RequestHeaders:  "Origin, Authorization, Content-Type",
		ExposedHeaders:  "",
		MaxAge:          50 * time.Second,
		Credentials:     true,
		ValidateHeaders: false,
	}))

	r.POST("/v1/register", q.postRegister)
	r.POST("/v1/login", q.postLogin)
	r.GET("/v1/blocks", q.getBlocks)
	r.GET("/v1/block/:hash", q.getBlock)
	r.GET("/v1/common", q.getCommon)
	r.GET("/v1/services", q.getServices)
	r.GET("/v1/createpayout/:currency", q.getCreatePayout)
	r.POST("/v1/payout", q.postPayout)

	api := r.Group("/v1/user/")
	api.Use(q.authMiddleware)
	{
		api.POST("tfa", q.postTFA)
		api.POST("tfa_setup", q.postTFASetup)
		api.POST("setpayout", q.postSetPayout)
		api.POST("changepass", q.postChangePassword)

		api.GET("workers", q.getWorkers)
		api.GET("unpaid", q.getUnpaid)
		api.GET("payouts", q.getPayouts)
		api.GET("payout/:hash", q.getPayout)
		api.GET("me", q.getMe)
	}

	q.engine = r
}

func (q *NgWebAPI) WatchStratum() {
	updates, err := q.service.ServiceWatcher("stratum")
	if err != nil {
		log.Crit("Failed to start coinserver watcher", "err", err)
		os.Exit(1)
	}
	go func() {
		q.log.Info("Listening for new stratum services")
		for {
			update := <-updates
			q.stratumsMtx.Lock()
			switch update.Action {
			case "removed":
				delete(q.stratums, update.ServiceID)
			case "updated", "added":
				q.stratums[update.ServiceID] = update.Status
			default:
				q.log.Warn("Unrecognized action from service watcher", "action", update.Action)
			}
			clients := map[string][]*common.StratumClientStatus{}
			for _, rawStatus := range q.stratums {
				var status common.StratumStatus
				err := mapstructure.Decode(rawStatus.Status, &status)
				if err != nil {
					q.log.Error("Invalid type in stratum status clients", "err", err)
					continue
				}
				for _, client := range status.Clients {
					clients[client.Username] = append(clients[client.Username], &client)
				}
			}
			q.stratumClients = clients
			q.stratumsMtx.Unlock()

		}
	}()
}

func (q *NgWebAPI) WatchCoinservers() {
	updates, err := q.service.ServiceWatcher("coinserver")
	if err != nil {
		log.Crit("Failed to start coinserver watcher", "err", err)
		os.Exit(1)
	}
	go func() {
		q.log.Info("Listening for new coinserver services")
		for {
			update := <-updates
			labels := update.Status.Labels
			currency := labels["currency"]

			q.currencyRPCMtx.Lock()
			q.coinserversMtx.Lock()
			switch update.Action {
			case "removed":
				delete(q.currencyRPC, currency)
				delete(q.coinservers, update.ServiceID)
			case "updated":
				q.coinservers[update.ServiceID] = update.Status
			case "added":
				endpoint := labels["endpoint"]
				connCfg := &rpcclient.ConnConfig{
					Host:         endpoint[7:] + "rpc",
					HTTPPostMode: true, // Bitcoin core only supports HTTP POST mode
					DisableTLS:   true, // Bitcoin core does not provide TLS by default
				}
				client, err := rpcclient.New(connCfg, nil)
				if err != nil {
					q.log.Error("Failed to init RPC client obj", "err", err)
				} else {
					q.currencyRPC[currency] = client
				}

				q.coinservers[update.ServiceID] = update.Status
			default:
				q.log.Warn("Unrecognized action from service watcher", "action", update.Action)
			}
			q.coinserversMtx.Unlock()
			q.currencyRPCMtx.Unlock()
		}
	}()
}

func projectBase() string {
	_, b, _, _ := runtime.Caller(0)
	basepath := filepath.Dir(b)
	return filepath.Join(basepath, "../../")
}

func (q *NgWebAPI) LoadFixtures(fixtures ...string) {
	for _, fileName := range fixtures {
		file, err := ioutil.ReadFile(filepath.Join(projectBase(), "sql", fileName) + ".sql")
		if err != nil {
			panic(err)
		}
		commands := strings.Split(string(file), ";")

		for _, command := range commands {
			_, err := q.db.Exec(command)
			if err != nil {
				q.log.Crit("Failed to exec", "sql", command)
				panic(err)
			}
		}
	}
}

func (q *NgWebAPI) getRPC(currency string) (*rpcclient.Client, bool) {
	q.currencyRPCMtx.Lock()
	defer q.currencyRPCMtx.Unlock()
	r, o := q.currencyRPC[currency]
	return r, o
}
