package main

import (
	"encoding/json"
	"github.com/coreos/etcd/client"
	"github.com/dustin/go-broadcast"
	"github.com/gin-gonic/gin"
	"github.com/icook/btcd/btcjson"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	_ "github.com/spf13/viper/remote"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type CoinBuddy struct {
	config        *viper.Viper
	cs            *Coinserver
	etcd          client.Client
	etcdKeys      client.KeysAPI
	blockListener *http.Server
	eventListener *gin.Engine
	lastBlock     json.RawMessage
	lastBlockMtx  sync.RWMutex
	broadcast     broadcast.Broadcaster
}

func NewCoinBuddy(configFile string) *CoinBuddy {
	config := viper.New()

	config.SetDefault("LogLevel", "info")
	config.SetDefault("BlockListenerBind", "localhost:3000")
	config.SetDefault("EventListenerBind", "localhost:4000")
	config.SetDefault("EtcdEndpoint", "http://127.0.0.1:4001")

	// Load from Env
	config.AutomaticEnv()

	serviceID := config.GetString("ServiceID")
	if serviceID != "" {
		log.Infof("Loaded service ID %s, pulling config from etcd", serviceID)
		config.AddRemoteProvider("etcd", config.GetString("EtcdEndpoint"), "/config/"+serviceID)
		config.SetConfigType("yaml")
		err := config.ReadRemoteConfig()
		if err != nil {
			log.WithError(err).Warn("Unable to load from etcd")
		}
	} else {
		// Load from file
		config.SetConfigName(configFile)
		config.AddConfigPath(".")
		config.SetConfigType("yaml")
		err := config.ReadInConfig()
		if err != nil {
			log.Fatalf("error %v on parsing configuration file", err)
		}
	}
	cb := &CoinBuddy{
		config:       config,
		broadcast:    broadcast.NewBroadcaster(10),
		lastBlockMtx: sync.RWMutex{},
	}

	levelConfig := config.GetString("LogLevel")
	level, err := log.ParseLevel(levelConfig)
	if err != nil {
		log.WithError(err).Fatal("Unable to parse log level %s", levelConfig)
	}
	log.SetLevel(level)

	cfg := client.Config{
		Endpoints: []string{config.GetString("EtcdEndpoint")},
		Transport: client.DefaultTransport,
		// set timeout per request to fail fast when the target endpoint is unavailable
		HeaderTimeoutPerRequest: time.Second,
	}
	cb.etcd, err = client.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	cb.etcdKeys = client.NewKeysAPI(cb.etcd)

	return cb
}

func (c *CoinBuddy) RunEventListener() {
	c.eventListener = gin.Default()
	c.eventListener.GET("/blocks", func(ctx *gin.Context) {
		listener := make(chan interface{})
		c.broadcast.Register(listener)
		defer func() {
			c.broadcast.Unregister(listener)
			close(listener)
		}()
		ctx.Stream(func(w io.Writer) bool {
			ctx.SSEvent("message", <-listener)
			return true
		})
		c.lastBlockMtx.RLock()
		if c.lastBlock != nil {
			listener <- c.lastBlock
		}
		c.lastBlockMtx.RUnlock()
	})

	go c.eventListener.Run(c.config.GetString("EventListenerBind"))
	log.Infof("Listening for subscriptions on http://%s/blocks",
		c.config.GetString("EventListenerBind"))
}

func (c *CoinBuddy) RunBlockListener() {
	c.blockListener = &http.Server{
		Addr: c.config.GetString("BlockListenerBind"),
	}
	updateBlock := func() error {
		params := []json.RawMessage{}
		template, err := c.cs.client.RawRequest("getblocktemplate", params)
		if err != nil {
			log.WithError(err).Error("Failed to get block template")
			if jerr, ok := err.(*btcjson.RPCError); ok {
				log.Infof("got rpc code %v from server", jerr.Code)
			}
			return err
		} else {
			log.Info("Got new block template from client")
			c.lastBlockMtx.Lock()
			c.lastBlock = template
			c.lastBlockMtx.Unlock()
			c.broadcast.Submit(template)
		}
		return nil
	}
	http.HandleFunc("/notif", func(w http.ResponseWriter, r *http.Request) {
		bid := r.URL.Query().Get("id")
		log.Infof("Got notif about new block ID=%s from server", bid)
		err := updateBlock()
		if err != nil {
			w.WriteHeader(http.StatusExpectationFailed)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	})
	go func() {
		if err := c.blockListener.ListenAndServe(); err != nil {
			// cannot panic, because this probably is an intentional close
			log.Infof("Httpserver: ListenAndServe() error: %s", err)
		}
	}()
	log.Infof("Listening for new block notifications on http://%s/notif",
		c.config.GetString("BlockListenerBind"))

	// Loop and try to get an initial block template every few seconds
	go func() {
		for {
			if c.lastBlock != nil {
				break
			}
			updateBlock()
			log.Info("Retrying initial block template fetch in 5")
			time.Sleep(5 * time.Second)
		}
	}()
}

func (c *CoinBuddy) RunCoinserver() error {
	// Parse the config to format for coinserver
	cfg := c.config.GetStringMap("NodeConfig")
	cfgProc := map[string]string{}
	for k, v := range cfg {
		key := strings.ToLower(k)
		cfgProc[key] = v.(string)
	}
	blocknotify := "/usr/bin/curl http://" + c.config.GetString("BlockListenerBind") + "/notif?id=%s"
	c.cs = NewCoinserver(cfgProc, blocknotify)

	go c.cs.Run()
	return c.cs.WaitUntilUp()
}

func (c *CoinBuddy) RunEtcdHealth() error {
	return nil
}

func (c *CoinBuddy) Stop() {
	if c.cs != nil {
		log.Info("Stopping coinserver")
		c.cs.Stop()
	}
	if c.broadcast != nil {
		log.Info("Shutting down broadcaster")
		c.broadcast.Close()
	}
}
