package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	config.SetDefault("BlockListenerBind", "127.0.0.1:3000")
	config.SetDefault("EventListenerBind", "127.0.0.1:4000")
	config.SetDefault("EtcdEndpoint", "http://127.0.0.1:4001")
	config.SetDefault("CurrencyCode", "BTC")
	config.SetDefault("HashingAlgo", "sha256d")
	config.SetDefault("CoinserverBinary", "bitcoind")
	config.SetDefault("NodeConfig.rpcuser", "admin1")
	config.SetDefault("NodeConfig.rpcpassword", "123")
	config.SetDefault("NodeConfig.port", "19000")
	config.SetDefault("NodeConfig.rpcport", "19001")
	config.SetDefault("NodeConfig.server", "1")

	// Load from Env so we can access etcd
	config.AutomaticEnv()

	// Load from etcd if the user specifies a serviceID, otherwise try config.yaml
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
			log.WithError(err).Fatalf("failed to parse configuration file '%s.yaml'", configFile)
		}
	}

	// Load from Env, which will overwrite everything else
	config.AutomaticEnv()

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
	log.Info("Set log level to ", level)
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
	gin.SetMode("release")
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
			out := base64.StdEncoding.EncodeToString(in.(json.RawMessage))
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
	c.cs = NewCoinserver(cfgProc, blocknotify)

	go c.cs.Run()
	return c.cs.WaitUntilUp()
}

func (c *CoinBuddy) RunEtcdHealth() error {
	// conn, err := net.Dial("udp", "8.8.8.8:80")
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// defer conn.Close()

	// localAddr := conn.LocalAddr().(*net.UDPAddr)
	// localIP := localAddr.IP
	// log.WithField("ip", localIP).Info("Detected routable IP address")

	go func() {
		var (
			lastStatus string
			serviceID  string = c.config.GetString("ServiceID")
		)
		for {
			time.Sleep(time.Second * 10)
			statusRaw, err := json.Marshal(map[string]interface{}{
				"algo":     c.config.GetString("HashingAlgo"),
				"currency": c.config.GetString("CurrencyCode"),
				"endpoint": fmt.Sprintf("http://%s/", c.config.GetString("EventListenerBind")),
			})
			status := string(statusRaw)
			if err != nil {
				log.WithError(err).Error("Failed serialization of status update")
				continue
			}
			opt := &client.SetOptions{
				TTL: time.Second * 15,
			}
			// Don't update if no new information, just refresh TTL
			if status == lastStatus {
				opt.Refresh = true
				opt.PrevExist = client.PrevExist
				status = ""
			} else {
				lastStatus = status
			}
			_, err = c.etcdKeys.Set(
				context.Background(), "/services/coinservers/"+serviceID, status, opt)
			if err != nil {
				log.WithError(err).Warn("Failed to update etcd status entry")
				continue
			}
		}
	}()
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
