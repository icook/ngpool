package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/dustin/go-broadcast"
	"github.com/gin-gonic/gin"
	"github.com/icook/ngpool/pkg/service"
	log "github.com/inconshreveable/log15"
	"github.com/pkg/errors"
	"github.com/spf13/viper"
	_ "github.com/spf13/viper/remote"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type CoinBuddy struct {
	config          *viper.Viper
	cs              *Coinserver
	blockListener   *http.Server
	eventListener   *gin.Engine
	lastBlock       json.RawMessage
	lastBlockHeight uint64
	lastBlockMtx    sync.RWMutex
	broadcast       broadcast.Broadcaster
	templateExtras  []byte
	service         *service.Service
}

func NewCoinBuddy() *CoinBuddy {
	cb := &CoinBuddy{
		broadcast:    broadcast.NewBroadcaster(10),
		lastBlockMtx: sync.RWMutex{},
	}
	return cb
}

func (c *CoinBuddy) ConfigureService(name string, etcdEndpoints []string) {
	c.service = service.NewService("coinserver", etcdEndpoints)
	c.config = c.service.LoadCommonConfig()
	c.service.LoadServiceConfig(c.config, name)
}

func (c *CoinBuddy) ParseConfig() {
	c.config.SetDefault("CoinserverBinary", "bitcoind")
	c.config.SetDefault("TemplateType", "getblocktemplate")
	c.config.SetDefault("CurrencyCode", "BTC")
	c.config.SetDefault("HashingAlgo", "sha256d")

	c.config.SetDefault("LogLevel", "info")
	c.config.SetDefault("BlockListenerBind", "127.0.0.1:3000")
	c.config.SetDefault("EventListenerBind", "127.0.0.1:4000")
	c.config.SetDefault("NodeConfig.rpcuser", "admin1")
	c.config.SetDefault("NodeConfig.rpcpassword", "123")
	c.config.SetDefault("NodeConfig.port", "19000")
	c.config.SetDefault("NodeConfig.rpcport", "19001")
	c.config.SetDefault("NodeConfig.server", "1")
	c.config.SetDefault("NodeConfig.datadir", "~/.bitcoin")

	levelConfig := c.config.GetString("LogLevel")
	level, err := log.LvlFromString(levelConfig)
	if err != nil {
		log.Crit("Unable to parse log level", "configval", levelConfig, "err", err)
		os.Exit(1)
	}
	handler := log.CallerFileHandler(log.StdoutHandler)
	handler = log.LvlFilterHandler(level, handler)
	log.Root().SetHandler(handler)
	log.Info("Set log level", "level", level)
}

// Starts all routines associated with this service. Non-blocking
func (c *CoinBuddy) Run() {
	err := c.RunCoinserver()
	if err != nil {
		log.Crit("Coinserver never came up for 90 seconds", "err", err)
		os.Exit(1)
	}
	c.generateTemplateExtras()
	c.RunBlockListener()
	c.RunEventListener()
	go c.service.KeepAlive(map[string]string{
		"algo":          c.config.GetString("HashingAlgo"),
		"currency":      c.config.GetString("CurrencyCode"),
		"endpoint":      fmt.Sprintf("http://%s/", c.config.GetString("EventListenerBind")),
		"template_type": c.config.GetString("TemplateType"),
	})
}

// We assemble a byte string of extra data to put in the getblocktemplate call
// currently used to inject chainid for aux networks, since this is the only
// missing value from GBT to generate aux blocks
func (c *CoinBuddy) generateTemplateExtras() {
	templateExtras := map[string]interface{}{}
	if c.config.GetString("TemplateType") == "getblocktemplate_aux" {
		params := []json.RawMessage{}
		resp, err := c.cs.client.RawRequest("getauxblock", params)
		if err != nil {
			log.Crit("Failed to run getauxblock, are you sure this coin is merge mineable?",
				"err", err)
			os.Exit(1)
		}
		var auxWork map[string]interface{}
		err = json.Unmarshal(resp, &auxWork)
		if err != nil {
			log.Crit("Failed to deserialize getauxblock", "err", err)
			os.Exit(1)
		}
		templateExtras["chainid"] = auxWork["chainid"]
	}
	serialized, err := json.Marshal(templateExtras)
	if err != nil {
		log.Crit("Error serializing templateExtras", "err", err)
		os.Exit(1)
	}
	c.templateExtras = []byte{}
	c.templateExtras = append(c.templateExtras, []byte(",\"extras\":")...)
	c.templateExtras = append(c.templateExtras, serialized...)
	c.templateExtras = append(c.templateExtras, '}')
}

func (c *CoinBuddy) RunEventListener() {
	gin.SetMode("release")
	c.eventListener = gin.Default()
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
			if jerr, ok := err.(*btcjson.RPCError); ok {
				log.Debug("error from rpc proxy", "err", err)
				ctx.JSON(500, gin.H{
					"result": nil,
					"error": map[string]interface{}{
						"code":    jerr.Code,
						"message": jerr.Message,
					}, "id": req.ID})
			} else {
				log.Warn("unknown error from rpc proxy", "err", err)
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
		// If we just got a block, make sure we re-run update block in case
		// push block is failing. This sometimes happens with fast block
		// submission (regtest), probably a race condition in coinserver code
		if req.Method == "submitblock" {
			c.UpdateBlock()
		}
	})
	c.eventListener.GET("/blocks", func(ctx *gin.Context) {
		listener := make(chan interface{})
		log.Debug("Registering new block listener")
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
	log.Info("Listening for SSE subscriptions", "endpoint", endpoint)
}

func (c *CoinBuddy) UpdateBlock() error {
	params := []json.RawMessage{}
	rawTemplate, err := c.cs.client.RawRequest("getblocktemplate", params)
	if err != nil {
		log.Error("Failed to get block template", "err", err)
		if jerr, ok := err.(*btcjson.RPCError); ok {
			log.Info("got rpc error from server", "code", jerr.Code)
		}
		return err
	} else {
		var template BlockTemplate
		err := json.Unmarshal(rawTemplate, &template)
		if err != nil {
			log.Warn("Malformed template", "tmpl", rawTemplate)
			return errors.New("Malformed template")
		}
		log.Info("Got new block template from client", "height", template.Height)

		rawTemplate = append(rawTemplate[:len(rawTemplate)-1], c.templateExtras...)
		var transmit bool = false
		c.lastBlockMtx.Lock()
		if template.Height > c.lastBlockHeight {
			c.lastBlockHeight = template.Height
			c.lastBlock = rawTemplate
			transmit = true
		}
		c.lastBlockMtx.Unlock()
		if transmit {
			c.broadcast.Submit(rawTemplate)
		}
	}
	return nil
}

type BlockTemplate struct {
	Height uint64
}

func (c *CoinBuddy) RunBlockListener() {
	c.blockListener = &http.Server{
		Addr: c.config.GetString("BlockListenerBind"),
	}
	http.HandleFunc("/notif", func(w http.ResponseWriter, r *http.Request) {
		bid := r.URL.Query().Get("id")
		log.Info("Got notif about new block from server", "hash", bid)
		err := c.UpdateBlock()
		if err != nil {
			w.WriteHeader(http.StatusExpectationFailed)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	})
	go func() {
		if err := c.blockListener.ListenAndServe(); err != nil {
			// cannot panic, because this probably is an intentional close
			log.Warn("Httpserver: ListenAndServe()", "err", err)
		}
	}()
	endpoint := fmt.Sprintf("http://%s/notif", c.config.GetString("BlockListenerBind"))
	log.Info("Listening for new block notifications", "endpoint", endpoint)

	// Loop and try to get an initial block template every few seconds
	go func() {
		for {
			if c.lastBlock != nil {
				break
			}
			c.UpdateBlock()
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
		log.Crit("Failed to start coinserver", "err", err)
		os.Exit(1)
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
