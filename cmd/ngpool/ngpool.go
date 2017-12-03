package main

import (
	"fmt"
	"github.com/coreos/etcd/client"
	"github.com/dustin/go-broadcast"
	"github.com/icook/ngpool/pkg/service"
	"github.com/r3labs/sse"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	_ "github.com/spf13/viper/remote"
	"net/http"
	"time"
)

type TemplateKey struct {
	Algo         string
	Currency     string
	TemplateType string
}

type Ngpool struct {
	config             *viper.Viper
	etcd               client.Client
	etcdKeys           client.KeysAPI
	coinserverWatchers map[string]*CoinserverWatcher
	stratumPorts       map[int]*StratumServer
	templateCast       map[TemplateKey]broadcast.Broadcaster
}

func NewNgpool() *Ngpool {
	config := viper.New()

	config.SetDefault("LogLevel", "info")
	config.SetDefault("Ports", []string{})
	// Load from Env so we can access etcd
	config.AutomaticEnv()

	ng := &Ngpool{
		config:             config,
		coinserverWatchers: make(map[string]*CoinserverWatcher),
		templateCast:       make(map[TemplateKey]broadcast.Broadcaster),
	}

	return ng
}

func (n *Ngpool) Start(service *service.Service) {
	levelConfig := n.config.GetString("LogLevel")
	level, err := log.ParseLevel(levelConfig)
	if err != nil {
		log.WithError(err).Fatal("Unable to parse log level %s", levelConfig)
	}
	log.Info("Set log level to ", level)
	log.SetLevel(level)

	updates, err := service.ServiceWatcher("coinserver")
	if err != nil {
		log.WithError(err).Fatal("Failed to start coinserver watcher")
	}
	go n.HandleCoinserverWatcherUpdates(updates)

	// Start each of the configured ports
	for port, _ := range n.config.GetStringMap("Ports") {
		subConfig := n.config.Sub(fmt.Sprintf("Ports.%s", port))
		ss := NewStratumServer(subConfig, n.getCurrencyCast)
		ss.Start()
	}
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

func (cw *CoinserverWatcher) Stop() {
	// Trigger the stopping of the watcher, and wait for complete shutdown (it
	// will close channel 'done' on exit)
	if cw.done == nil {
		return
	}
	cw.done <- ""
	<-cw.done
}

func (cw *CoinserverWatcher) Run() {
	cw.done = make(chan interface{})
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
					cw.currencyCast.Submit(string(lastEvent.Data))
					if cw.status != "live" {
						log.Info("CoinserverWatcher is now LIVE")
					}
					cw.status = "live"
				}
			}
		}
	}
}

func (n *Ngpool) HandleCoinserverWatcherUpdates(updates chan service.ServiceStatusUpdate) {
	log.Infof("Listening for new coinserver services")
	for {
		update := <-updates
		switch update.Action {
		case "removed":
			if csw, ok := n.coinserverWatchers[update.ServiceID]; ok {
				log.Info("Coinserver shutdown ", update.ServiceID)
				csw.Stop()
			}
		case "updated":
			log.Infof("Coinserver status update: %s %+v", update.ServiceID, update.Status)
		case "added":
			labels := update.Status.Labels
			// TODO: Should probably serialize to datatype...
			currencyCast := n.getCurrencyCast(TemplateKey{
				Currency:     labels["currency"].(string),
				Algo:         labels["algo"].(string),
				TemplateType: labels["template_type"].(string),
			})
			cw := &CoinserverWatcher{
				endpoint:     labels["endpoint"].(string),
				status:       "starting",
				currencyCast: currencyCast,
				id:           update.ServiceID,
			}
			n.coinserverWatchers[update.ServiceID] = cw
			go cw.Run()
			log.Infof("New coinserver detected: %s %+v", update.ServiceID, update.Status)
		default:
			log.Warn("Unrecognized action from service watcher ", update.Action)
		}
	}
}

func (n *Ngpool) getCurrencyCast(key TemplateKey) broadcast.Broadcaster {
	if _, ok := n.templateCast[key]; !ok {
		n.templateCast[key] = broadcast.NewBroadcaster(10)
	}
	return n.templateCast[key]
}
