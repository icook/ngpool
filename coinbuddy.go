package main

import (
	"encoding/json"
	"fmt"
	"github.com/dustin/go-broadcast"
	"github.com/gin-gonic/gin"
	"github.com/icook/btcd/btcjson"
	"github.com/icook/btcd/rpcclient"
	"github.com/mitchellh/go-homedir"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Coinserver struct {
	Config  map[string]string
	client  *rpcclient.Client
	command *exec.Cmd
}

func NewCoinserver(overrideConfig map[string]string, blocknotify string) *Coinserver {
	// Set some defaults
	config := map[string]string{
		"rpcuser":     "admin1",
		"rpcpassword": "123",
		"port":        "19000",
		"rpcport":     "19001",
		"server":      "1",
		"blocknotify": blocknotify,
	}
	for key, val := range overrideConfig {
		config[key] = val
	}
	dir, _ := homedir.Expand(config["datadir"])
	config["datadir"] = dir
	config["pid"] = path.Join(config["datadir"], "coinserver.pid")
	args := []string{}
	for key, val := range config {
		args = append(args, "-"+key+"="+val)
	}
	log.Debug("Starting coinserver with config ", args)
	c := &Coinserver{
		Config:  config,
		command: exec.Command("litecoind", args...),
	}

	connCfg := &rpcclient.ConnConfig{
		Host:         fmt.Sprintf("localhost:%v", config["rpcport"]),
		User:         config["rpcuser"],
		Pass:         config["rpcpassword"],
		HTTPPostMode: true, // Bitcoin core only supports HTTP POST mode
		DisableTLS:   true, // Bitcoin core does not provide TLS by default
	}
	client, err := rpcclient.New(connCfg, nil)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize rpcclient")
	}
	c.client = client

	// Try to stop a coinserver from prvious run
	err = c.kill()
	if err == nil {
		log.Info("Killed coinserver that was still running")
	} else {
		log.Debug("Failed to kill previous run of coinserver: ", err)
	}

	return c
}

func (c *Coinserver) Stop() {
	err := c.kill()
	if err != nil {
		log.Warn("Unable to stop coinserver: ", err)
		return
	}
}

func (c *Coinserver) kill() error {
	pidStr, err := ioutil.ReadFile(c.Config["pid"])
	if err != nil {
		return errors.Wrap(err, "Can't load coinserver pidfile")
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(string(pidStr)), 10, 32)
	if err != nil {
		return errors.Wrap(err, "Can't parse coinserver pidfile")
	}
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return errors.Wrap(err, "No process on stop, exiting")
	}
	err = proc.Kill()
	if err != nil {
		return errors.Wrap(err, "Error exiting process")
	}
	time.Sleep(1 * time.Second)
	// There's an annoying race condition here if we check the err value. If
	// the process exits before wait call runs then we get an error that is
	// annoying to test for, so we just don't worry about it
	proc.Wait()
	log.Info("Killed (hopefully) pid ", pid)
	return nil
}

func (c *Coinserver) Run() error {
	return c.command.Run()
}

func (c *Coinserver) WaitUntilUp() error {
	var err error
	i := 0
	tot := 900
	for ; i < tot; i++ {
		ret, err := c.client.GetInfo()
		if err == nil {
			log.Infof("Coinserver up after %v", i/10)
			log.Infof("\t-> getinfo=%+v", ret)
			break
		}
		time.Sleep(100 * time.Millisecond)
		if i%50 == 0 && i > 0 {
			log.Infof("Coinserver not up yet after %d / %d seconds", i/10, tot/10)
		}
	}
	return err
}

type CoinBuddy struct {
	config        *viper.Viper
	cs            *Coinserver
	blockListener *http.Server
	eventListener *gin.Engine
	lastBlock     json.RawMessage
	lastBlockMtx  sync.RWMutex
	broadcast     broadcast.Broadcaster
}

func NewCoinBuddy(configFile string) *CoinBuddy {
	log.SetFormatter(&log.TextFormatter{ForceColors: true})
	log.SetLevel(log.DebugLevel)

	config := viper.New()
	config.SetEnvPrefix("relay")
	config.AutomaticEnv()
	config.AddConfigPath(".")
	config.SetConfigType("yaml")
	config.SetConfigName(configFile)
	// The port we listen for notification from coinserver
	config.SetDefault("BlockListenerBind", "localhost:3000")
	// The port we send out SSE events for new blocks
	config.SetDefault("EventListenerBind", "localhost:4000")
	err := config.ReadInConfig()
	if err != nil {
		log.Fatalf("error %v on parsing configuration file", err)
	}
	return &CoinBuddy{
		config:       config,
		broadcast:    broadcast.NewBroadcaster(10),
		lastBlockMtx: sync.RWMutex{},
	}
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

func (c *CoinBuddy) RunNode() error {
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

func main() {
	cb := NewCoinBuddy("config")
	defer cb.Stop()
	err := cb.RunNode()
	if err != nil {
		log.WithError(err).Fatal("Coinserver never came up for 90 seconds")
	}
	cb.RunBlockListener()
	cb.RunEventListener()

	// Wait until we recieve sigint
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	// Defered cleanup is performed now
}
