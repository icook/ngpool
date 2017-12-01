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
	"strings"
	"time"
)

type Ngpool struct {
	config             *viper.Viper
	etcd               client.Client
	etcdKeys           client.KeysAPI
	coinserverWatchers map[string]*CoinserverWatcher
	currencyCast       map[string]broadcast.Broadcaster
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
		currencyCast:       make(map[string]broadcast.Broadcaster),
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
	client := sse.NewClient(cw.endpoint)

	client.Subscribe("block", func(msg *sse.Event) {
		log.Debugf("Got new block from %s: %v", cw.endpoint, msg)
		cw.currencyCast.Submit(msg)
	})
}

func (n *Ngpool) getCurrencyCast(currency string) broadcast.Broadcaster {
	if _, ok := n.currencyCast[currency]; !ok {
		n.currencyCast[currency] = broadcast.NewBroadcaster(10)
	}
	return n.currencyCast[currency]
}

func (n *Ngpool) addCoinserverWatcher(node *client.Node) error {
	lbi := strings.LastIndexByte(node.Key, '/') + 1
	serviceID := node.Key[lbi:]
	type Status struct {
		Endpoint string
		Currency string
	}
	var info Status
	err := json.Unmarshal([]byte(node.Value), &info)
	if err != nil {
		log.WithError(err).Warn("Invalid status from service")
		return err
	}
	log.Infof("Node from listing %+v : %+v", serviceID, info)

	cw := &CoinserverWatcher{
		endpoint:     info.Endpoint,
		status:       "starting",
		done:         make(chan interface{}),
		currencyCast: n.getCurrencyCast(info.Currency),
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
	opt := &client.GetOptions{
		Recursive: true,
	}
	res, err := n.etcdKeys.Get(context.Background(), "/services/coinservers", opt)
	if err != nil {
		log.WithError(err).Fatal("Unable to contact etcd")
	}
	for _, node := range res.Node.Nodes {
		n.addCoinserverWatcher(node)
	}
}
