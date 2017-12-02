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
	algo         string
	currency     string
	templateType string
}

type Ngpool struct {
	config             *viper.Viper
	etcd               client.Client
	etcdKeys           client.KeysAPI
	coinserverWatchers map[string]*CoinserverWatcher
	templateCast       map[TemplateKey]broadcast.Broadcaster
}

func NewNgpool() *Ngpool {
	config := viper.New()

	config.SetDefault("LogLevel", "info")
	// Load from Env so we can access etcd
	config.AutomaticEnv()

	ng := &Ngpool{
		config:             config,
		coinserverWatchers: make(map[string]*CoinserverWatcher),
		templateCast:       make(map[TemplateKey]broadcast.Broadcaster),
	}

	return ng
}

func (n *Ngpool) Run() {
	levelConfig := n.config.GetString("LogLevel")
	level, err := log.ParseLevel(levelConfig)
	if err != nil {
		log.WithError(err).Fatal("Unable to parse log level %s", levelConfig)
	}
	log.Info("Set log level to ", level)
	log.SetLevel(level)

	n.StartCoinserverDiscovery()
}

func (n *Ngpool) Stop() {
}

type CoinserverWatcher struct {
	id           string
	endpoint     string
	status       string
	meta         map[string]interface{}
	lastBlock    time.Time
	currencyCast broadcast.Broadcaster
	done         chan interface{}
}

func (cw *CoinserverWatcher) Stop() {
	// Trigger the stopping of the watcher, and wait for complete shutdown (it
	// will close channel 'done' on exit)
	cw.done <- ""
	<-cw.done
}

func (cw *CoinserverWatcher) Run() {
	defer close(cw.done)
	client := &sse.Client{
		URL:            cw.endpoint + "blocks",
		EncodingBase64: true,
		Connection:     &http.Client{},
		Headers:        make(map[string]string),
	}

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
			// Wait for new event or exit signal
			select {
			case <-cw.done:
				return
			case msg := <-events:
				// When the connection breaks we get a nill pointer. Break out
				// of loop and try to reconnect
				if msg == nil {
					break
				}
				// Unfortunately this SSE library produces an event for every
				// line, instead of an event for every \n\n as is the standard.
				// So we manually combine the events into one
				if msg.Event != nil {
					lastEvent.Event = msg.Event
				}
				if msg.Data != nil {
					lastEvent.Data = msg.Data
					log.Debugf("Got new template from %s: %s '%s'",
						cw.endpoint, lastEvent.Event, lastEvent.Data)
					cw.currencyCast.Submit(lastEvent)
					if cw.status != "live" {
						log.Info("CoinserverWatcher is now LIVE")
					}
					cw.status = "live"
				}
			}
		}
	}
}

func (n *Ngpool) getCurrencyCast(currency string, algo string, templateType string) broadcast.Broadcaster {
	key := TemplateKey{currency: currency, algo: algo, templateType: templateType}
	if _, ok := n.templateCast[key]; !ok {
		n.templateCast[key] = broadcast.NewBroadcaster(10)
	}
	return n.templateCast[key]
}

func (n *Ngpool) addCoinserverWatcher(node *client.Node) error {
	// Parse all the node details about the watcher
	lbi := strings.LastIndexByte(node.Key, '/') + 1
	serviceID := node.Key[lbi:]
	type Status struct {
		Endpoint     string
		Currency     string
		Algo         string
		TemplateType string `json:"template_type"`
		Meta         map[string]interface{}
	}
	var info Status
	err := json.Unmarshal([]byte(node.Value), &info)
	if err != nil {
		log.WithError(err).Warn("Invalid status from service")
		return err
	}
	currencyCast := n.getCurrencyCast(info.Currency, info.Algo, info.TemplateType)

	// Check if we're already running a watcher for this ServiceID
	if csw, ok := n.coinserverWatchers[serviceID]; ok {
		if currencyCast != csw.currencyCast {
			// If essential information about the coinserver has changed, we
			// must restart the coinserver watcher, so stop the old one
			csw.Stop()
		} else {
			// The server information has simple been updated
			csw.meta = info.Meta
			return nil
		}
	}

	// Start a new coinserver watcher
	log.Infof("New node %+v : %+v", serviceID, info)
	cw := &CoinserverWatcher{
		endpoint:     info.Endpoint,
		status:       "starting",
		done:         make(chan interface{}),
		currencyCast: currencyCast,
		id:           serviceID,
		meta:         info.Meta,
	}
	n.coinserverWatchers[serviceID] = cw

	// Cleanup the coinserverWatchers map after exit of the watcher
	go func() {
		cw.Run()
		delete(n.coinserverWatchers, serviceID)
		log.Debug("Cleanup coinserver watcher ", serviceID)
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
		lbi := strings.LastIndexByte(res.Node.Key, '/') + 1
		serviceID := res.Node.Key[lbi:]
		log.Debugf("coinserver %s status key %s", serviceID, res.Action)
		if res.Action == "expire" {
			if csw, ok := n.coinserverWatchers[serviceID]; ok {
				log.Info("Coinserver shutdown ", serviceID)
				csw.Stop()
			}
		} else if res.Action == "set" || res.Action == "update" {
			n.addCoinserverWatcher(res.Node)
		}
	}
}
