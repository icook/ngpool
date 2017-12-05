package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	config         *viper.Viper
	cs             *Coinserver
	blockListener  *http.Server
	eventListener  *gin.Engine
	lastBlock      json.RawMessage
	lastBlockMtx   sync.RWMutex
	broadcast      broadcast.Broadcaster
	templateExtras []byte
}

func NewCoinBuddy() *CoinBuddy {
	config := viper.New()

	config.SetDefault("CoinserverBinary", "bitcoind")
	config.SetDefault("TemplateType", "getblocktemplate")
	config.SetDefault("CurrencyCode", "BTC")
	config.SetDefault("HashingAlgo", "sha256d")

	config.SetDefault("LogLevel", "info")
	config.SetDefault("BlockListenerBind", "127.0.0.1:3000")
	config.SetDefault("EventListenerBind", "127.0.0.1:4000")
	config.SetDefault("NodeConfig.rpcuser", "admin1")
	config.SetDefault("NodeConfig.rpcpassword", "123")
	config.SetDefault("NodeConfig.port", "19000")
	config.SetDefault("NodeConfig.rpcport", "19001")
	config.SetDefault("NodeConfig.server", "1")
	config.SetDefault("NodeConfig.datadir", "~/.bitcoin")

	// Load from Env, which will overwrite everything else
	config.AutomaticEnv()

	cb := &CoinBuddy{
		config:       config,
		broadcast:    broadcast.NewBroadcaster(10),
		lastBlockMtx: sync.RWMutex{},
	}

	return cb
}

func (c *CoinBuddy) Run() {
	levelConfig := c.config.GetString("LogLevel")
	level, err := log.ParseLevel(levelConfig)
	if err != nil {
		log.WithError(err).Fatal("Unable to parse log level %s", levelConfig)
	}
	log.Info("Set log level to ", level)
	log.SetLevel(level)

	err = c.RunCoinserver()
	if err != nil {
		log.WithError(err).Fatal("Coinserver never came up for 90 seconds")
	}
	c.generateTemplateExtras()
	c.RunBlockListener()
	c.RunEventListener()
}

func (c *CoinBuddy) generateTemplateExtras() {
	// We assemble a byte string of extra data to put in the getblocktemplate
	// call currently used to inject chainid for aux networks, since this is
	// the only missing value from GBT to generate aux blocks
	templateExtras := map[string]interface{}{}
	if c.config.GetString("TemplateType") == "getblocktemplate_aux" {
		params := []json.RawMessage{}
		resp, err := c.cs.client.RawRequest("getauxblock", params)
		if err != nil {
			log.WithError(err).Fatal(
				"Failed to run getauxblock, are you sure this coin is merge mineable?")
		}
		var auxWork map[string]interface{}
		err = json.Unmarshal(resp, &auxWork)
		if err != nil {
			log.WithError(err).Fatal("Failed to deserialize getauxblock")
		}
		templateExtras["chainid"] = auxWork["chainid"]
	}
	serialized, err := json.Marshal(templateExtras)
	if err != nil {
		log.WithError(err).Fatal("Error serializing templateExtras")
	}
	c.templateExtras = []byte{}
	c.templateExtras = append(c.templateExtras, []byte(",\"extras\":")...)
	c.templateExtras = append(c.templateExtras, serialized...)
	c.templateExtras = append(c.templateExtras, '}')
}

func (c *CoinBuddy) RunEventListener() {
	c.eventListener = gin.Default()
	gin.SetMode("release")
	c.eventListener.POST("/rpc", func(ctx *gin.Context) {
		type RPCReq struct {
			Method string
			Params []json.RawMessage
			ID     int
		}
		var req RPCReq
		ctx.BindJSON(&req)
		res, err := c.cs.client.RawRequest(req.Method, req.Params)
		if err != nil {
			log.WithError(err).Warn("error from rpc proxy")
			if jerr, ok := err.(*btcjson.RPCError); ok {
				ctx.JSON(500, gin.H{
					"result": nil,
					"error": map[string]interface{}{
						"code":    jerr.Code,
						"message": jerr.Message,
					}, "id": req.ID})
			} else {
				ctx.JSON(500, gin.H{
					"result": nil,
					"error": map[string]interface{}{
						"code":    -1000,
						"message": "Proxy error",
					}, "id": req.ID})
			}
			return
		}
		ctx.JSON(200, gin.H{"result": res, "error": nil, "id": req.ID})
	})
	c.eventListener.GET("/blocks", func(ctx *gin.Context) {
		listener := make(chan interface{})
		c.broadcast.Register(listener)
		defer func() {
			log.Debug("Closing client channel")
			c.broadcast.Unregister(listener)
			close(listener)
		}()
		// Send the latest block as soon as they connect
		go func() {
			c.lastBlockMtx.RLock()
			if c.lastBlock != nil {
				listener <- c.lastBlock
			}
			c.lastBlockMtx.RUnlock()
		}()
		ctx.Stream(func(w io.Writer) bool {
			in := <-listener
			strippedIn := bytes.TrimSpace(in.(json.RawMessage))
			out := base64.StdEncoding.EncodeToString(strippedIn)
			ctx.SSEvent("block", out)
			log.Debug("Sent block update to listener")
			return true
		})
	})

	go c.eventListener.Run(c.config.GetString("EventListenerBind"))
	endpoint := fmt.Sprintf("http://%s/blocks", c.config.GetString("EventListenerBind"))
	log.WithField("endpoint", endpoint).Infof("Listening for SSE subscriptions")
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
			template = append(template[:len(template)-1], c.templateExtras...)
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
	endpoint := fmt.Sprintf("http://%s/notif", c.config.GetString("BlockListenerBind"))
	log.WithField("endpoint", endpoint).Info("Listening for new block notifications")

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
	c.cs = NewCoinserver(cfgProc, blocknotify, c.config.GetString("CoinserverBinary"))

	err := c.cs.Run()
	if err != nil {
		log.WithError(err).Fatal("Failed to start coinserver")
	}
	return c.cs.WaitUntilUp()
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
