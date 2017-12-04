package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/btcsuite/btcd/blockchain"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcutil"
	"github.com/coreos/etcd/client"
	"github.com/dustin/go-broadcast"
	"github.com/icook/btcd/rpcclient"
	"github.com/icook/ngpool/pkg/service"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
	"github.com/r3labs/sse"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	_ "github.com/spf13/viper/remote"
	"golang.org/x/crypto/scrypt"
	"net/http"
	"sync"
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
	templateCast       map[TemplateKey]broadcast.Broadcaster
	templateCastMtx    *sync.Mutex
	jobCast            broadcast.Broadcaster
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
		jobCast:            broadcast.NewBroadcaster(10),
		templateCastMtx:    &sync.Mutex{},
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

	latestTemp := map[TemplateKey][]byte{}
	latestTempMtx := sync.Mutex{}

	var tmplKey TemplateKey
	val := n.config.Get("BaseCurrency")
	err = mapstructure.Decode(val, &tmplKey)
	if err != nil {
		log.WithError(err).Error("Invalid configuration, currencies of improper format")
		return
	}

	go func() {
		log.Infof("Registering listener for %+v", tmplKey)
		listener := make(chan interface{})
		broadcast := n.getCurrencyCast(tmplKey)
		broadcast.Register(listener)
		defer func() {
			log.Debug("Closing template listener channel")
			broadcast.Unregister(listener)
			close(listener)
		}()
		for {
			newTemplate := <-listener
			template, ok := newTemplate.([]byte)
			if !ok {
				log.Errorf("Got invalid type from template listener: %#v", newTemplate)
				continue
			}
			latestTempMtx.Lock()
			latestTemp[tmplKey] = template
			job, err := genJob(latestTemp)
			log.Info("Generated new job, pushing...")
			if err != nil {
				log.WithError(err).Error("Error generating job")
				continue
			}
			n.jobCast.Submit(job)
			latestTempMtx.Unlock()
		}
	}()

	go n.Miner()
}

func genJob(templates map[TemplateKey][]byte) (*Job, error) {
	for tmplKey, tmplRaw := range templates {
		if tmplKey.TemplateType != "getblocktemplate" {
			log.WithField("type", tmplKey.TemplateType).Error("Unrecognized template type")
			continue
		}

		var tmpl BlockTemplate
		err := json.Unmarshal(tmplRaw, &tmpl)
		if err != nil {
			return nil, errors.Wrapf(err, "Unable to deserialize template: %v", string(tmplRaw))
		}

		chainConfig, ok := CurrencyConfig[tmplKey.Currency]
		if !ok {
			return nil, errors.Wrapf(err, "No currency config for %s", tmplKey.Currency)
		}

		job, err := NewJobFromTemplate(&tmpl, chainConfig)
		if err != nil {
			return nil, errors.Wrap(err, "Error generating job")
		}
		return job, nil
	}
	return nil, errors.New("Not sufficient templates to build job")
}

func (n *Ngpool) Miner() {
	listener := make(chan interface{})
	n.jobCast.Register(listener)
	jobLock := sync.Mutex{}
	var job *Job

	// Watch for new jobs for us
	go func() {
		for {
			jobOrig := <-listener
			newJob, ok := jobOrig.(*Job)
			if newJob == nil || !ok {
				log.Printf("%#v", jobOrig)
				log.WithField("job", jobOrig).Warn("Bad job from broadcast")
				continue
			}
			jobLock.Lock()
			job = newJob
			jobLock.Unlock()
		}
	}()

	go func() {
		var (
			blockHash = &big.Int{}
		)

		var i uint32 = 0
		for {
			if 1%1000 == 0 {
				log.Info("1khash done")
			}
			if job == nil {
				time.Sleep(time.Second * 1)
				continue
			}
			jobLock.Lock()
			var nonce = make([]byte, 4)
			binary.BigEndian.PutUint32(nonce, i)

			// Static extranonce
			coinbase := job.getCoinbase(extraNonceMagic)
			header := job.getBlockHeader(nonce, extraNonceMagic, coinbase)

			headerHsh, err := scrypt.Key(header, header, 1024, 1, 1, 32)
			if err != nil {
				log.WithError(err).Error("Failed scrypt hash")
				panic(err)
			}
			hashObj, err := chainhash.NewHash(headerHsh)
			if err != nil {
				log.WithError(err).Error("Failed conversion to hash")
				panic(err)
			}

			if blockchain.HashToBig(hashObj).Cmp(job.target) <= 0 {
				block := job.getBlock(header, coinbase)
				log.Infof("Found a block! \n%x", block)
				return
			}
			jobLock.Unlock()
			i += 1
		}
	}()
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
		URL:        cw.endpoint + "blocks",
		Connection: &http.Client{},
		Headers:    make(map[string]string),
	}

	for {
		events := make(chan *sse.Event)
		err := client.SubscribeChan("messages", events)
		if err != nil {
			if cw.status != "down" {
				log.WithError(err).Warnf("CoinserverWatcher %s is now DOWN", cw.id)
			}
			cw.status = "down"
			select {
			case <-cw.done:
				return
			case <-time.After(time.Second * 2):
			}
			continue
		}
		lastEvent := sse.Event{}
		cw.status = "up"
		log.Infof("CoinserverWatcher %s is now UP", cw.id)
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
					decoded, err := base64.StdEncoding.DecodeString(string(msg.Data))
					if err != nil {
						log.WithField("payload", decoded).Warn("Bad payload from coinserver")
					}
					lastEvent.Data = decoded
					log.Debugf("Got new template from %s: %s '%s'",
						cw.endpoint, lastEvent.Event, lastEvent.Data)
					cw.currencyCast.Submit(lastEvent.Data)
					if cw.status != "live" {
						log.Infof("CoinserverWatcher %s is now LIVE", cw.id)
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
	n.templateCastMtx.Lock()
	if _, ok := n.templateCast[key]; !ok {
		n.templateCast[key] = broadcast.NewBroadcaster(10)
	}
	n.templateCastMtx.Unlock()
	return n.templateCast[key]
}
