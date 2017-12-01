package main

import (
	"context"
	"encoding/json"
	"github.com/coreos/etcd/client"
	"github.com/dustin/go-broadcast"
	"github.com/r3labs/sse"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	_ "github.com/spf13/viper/remote"
	"net/http"
	"strings"
	"time"
)

type TemplateKey struct {
	algo     string
	currency string
}

type Ngpool struct {
	config             *viper.Viper
	etcd               client.Client
	etcdKeys           client.KeysAPI
	coinserverWatchers map[string]*CoinserverWatcher
	templateCast       map[TemplateKey]broadcast.Broadcaster
}

func NewNgpool(configFile string) *Ngpool {
	config := viper.New()

	config.SetDefault("LogLevel", "info")
	config.SetDefault("EtcdEndpoint", "http://127.0.0.1:4001")
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

	ng := &Ngpool{
		config:             config,
		coinserverWatchers: make(map[string]*CoinserverWatcher),
		templateCast:       make(map[TemplateKey]broadcast.Broadcaster),
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
	ng.etcd, err = client.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	ng.etcdKeys = client.NewKeysAPI(ng.etcd)

	return ng
}

func (n *Ngpool) Stop() {
}

type CoinserverWatcher struct {
	id           string
	endpoint     string
	status       string
	lastBlock    time.Time
	currencyCast broadcast.Broadcaster
	done         chan interface{}
}

func (cw *CoinserverWatcher) Run() {
	client := &sse.Client{
		URL:            cw.endpoint + "blocks",
		EncodingBase64: true,
		Connection:     &http.Client{},
		Headers:        make(map[string]string),
	}
	defer close(cw.done)

	for {
		events := make(chan *sse.Event)
		err := client.SubscribeChan("messages", events)
		if err != nil {
			if cw.status != "down" {
				log.WithError(err).Warn("CoinserverWatcher is now DOWN")
			}
			cw.status = "down"
			time.Sleep(time.Second * 2)
			log.Debugf("Retrying CoinserverWatcher subscribe")
			continue
		}
		lastEvent := sse.Event{}
		cw.status = "up"
		log.Info("CoinserverWatcher is now UP")
		for {
			msg := <-events
			// Unfortunately this SSE library produces an event for every line,
			// instead of an event for every \n\n as is the standard. So we
			// manually combine the events into one
			if msg == nil {
				break
			}
			if msg.Event != nil {
				lastEvent.Event = msg.Event
			}
			if msg.Data != nil {
				lastEvent.Data = msg.Data
				log.Debugf("Got new block from %s: %s '%s'", cw.endpoint, lastEvent.Event, lastEvent.Data)
				cw.currencyCast.Submit(lastEvent)
				if cw.status != "live" {
					log.Info("CoinserverWatcher is now LIVE")
				}
				cw.status = "live"
			}
		}
	}
}

func (n *Ngpool) getCurrencyCast(currency string, algo string) broadcast.Broadcaster {
	key := TemplateKey{currency: currency, algo: algo}
	if _, ok := n.templateCast[key]; !ok {
		n.templateCast[key] = broadcast.NewBroadcaster(10)
	}
	return n.templateCast[key]
}

func (n *Ngpool) addCoinserverWatcher(node *client.Node) error {
	lbi := strings.LastIndexByte(node.Key, '/') + 1
	serviceID := node.Key[lbi:]
	type Status struct {
		Endpoint string
		Currency string
		Algo     string
	}
	var info Status
	err := json.Unmarshal([]byte(node.Value), &info)
	if err != nil {
		log.WithError(err).Warn("Invalid status from service")
		return err
	}
	log.Infof("New node %+v : %+v", serviceID, info)

	cw := &CoinserverWatcher{
		endpoint:     info.Endpoint,
		status:       "starting",
		done:         make(chan interface{}),
		currencyCast: n.getCurrencyCast(info.Currency, info.Algo),
		id:           serviceID,
	}
	n.coinserverWatchers[info.Endpoint] = cw
	go cw.Run()
	go func() {
		<-cw.done
		delete(n.coinserverWatchers, info.Endpoint)
		log.Debug("Cleanup coinserver watcher ", info.Endpoint)
	}()
	return nil
}

func (n *Ngpool) StartCoinserverDiscovery() {
	getOpt := &client.GetOptions{
		Recursive: true,
	}
	res, err := n.etcdKeys.Get(context.Background(), "/services/coinservers", getOpt)
	if err != nil {
		log.WithError(err).Fatal("Unable to contact etcd")
	}
	for _, node := range res.Node.Nodes {
		n.addCoinserverWatcher(node)
	}
	// Start a watcher for all changes after the pull we're doing
	watchOpt := &client.WatcherOptions{
		AfterIndex: res.Index,
		Recursive:  true,
	}
	watcher := n.etcdKeys.Watcher("/services/coinservers", watchOpt)
	for {
		res, err = watcher.Next(context.Background())
		if err != nil {
			log.WithError(err).Warn("Error from coinserver watcher")
			time.Sleep(time.Second * 2)
			continue
		}
		log.Infof("got change: %#v", res)
	}
}
