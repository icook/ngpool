package main

import (
	"encoding/base64"
	"encoding/binary"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
	"github.com/coreos/etcd/client"
	"github.com/dustin/go-broadcast"
	"github.com/icook/btcd/rpcclient"
	"github.com/icook/ngpool/pkg/service"
	"github.com/mitchellh/mapstructure"
	"github.com/r3labs/sse"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	_ "github.com/spf13/viper/remote"
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
	// Keyed by currency code
	blockCast     map[string]broadcast.Broadcaster
	blockCastMtx  *sync.Mutex
	jobCast       broadcast.Broadcaster
	latestTemp    map[TemplateKey][]byte
	latestTempMtx sync.Mutex
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

		templateCast:    make(map[TemplateKey]broadcast.Broadcaster),
		templateCastMtx: &sync.Mutex{},
		blockCast:       make(map[string]broadcast.Broadcaster),
		blockCastMtx:    &sync.Mutex{},
		jobCast:         broadcast.NewBroadcaster(10),
		latestTemp:      map[TemplateKey][]byte{},
		latestTempMtx:   sync.Mutex{},
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

	var tmplKey TemplateKey
	val := n.config.Get("BaseCurrency")
	err = mapstructure.Decode(val, &tmplKey)
	if err != nil {
		log.WithError(err).Error("Invalid configuration, 'BaseCurrency' of improper format")
		return
	}
	go n.listenTemplate(tmplKey)

	var tmplKeys []TemplateKey
	val = n.config.Get("AuxCurrencies")
	err = mapstructure.Decode(val, &tmplKeys)
	if err != nil {
		log.WithError(err).Error("Invalid configuration, 'AuxCurrency' of improper format")
		return
	}
	for _, tmplKey := range tmplKeys {
		go n.listenTemplate(tmplKey)
	}

	go n.Miner()
}

func (n *Ngpool) listenTemplate(tmplKey TemplateKey) {
	log.Infof("Registering listener for %+v", tmplKey)
	listener := make(chan interface{})
	broadcast := n.getTemplateCast(tmplKey)
	broadcast.Register(listener)
	defer func() {
		log.Debug("Closing template listener channel")
		broadcast.Unregister(listener)
		close(listener)
	}()
	for {
		newTemplate := <-listener
		log.Infof("Got new template on %+v", tmplKey)
		template, ok := newTemplate.([]byte)
		if !ok {
			log.Errorf("Got invalid type from template listener: %#v", newTemplate)
			continue
		}
		n.latestTempMtx.Lock()
		n.latestTemp[tmplKey] = template
		job, err := NewJobFromTemplates(n.latestTemp)
		if err != nil {
			log.WithError(err).Error("Error generating job")
			n.latestTempMtx.Unlock()
			continue
		}
		log.Info("New job successfully generated, pushing...")
		n.jobCast.Submit(job)
		n.latestTempMtx.Unlock()
	}
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
				log.WithField("job", jobOrig).Warn("Bad job from broadcast")
				continue
			}
			jobLock.Lock()
			job = newJob
			jobLock.Unlock()
		}
	}()

	go func() {
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

			solves, _, err := job.CheckSolves(nonce, extraNonceMagic, nil)
			if err != nil {
				log.WithError(err).Warn("Failed to check solves for job")
			}
			for currencyCode, block := range solves {
				n.blockCast[currencyCode].Submit(block)
			}
			if len(solves) > 0 {
				time.Sleep(time.Millisecond * 200)
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
	tmplKey      TemplateKey
	endpoint     string
	status       string
	currencyCast broadcast.Broadcaster
	blockCast    broadcast.Broadcaster
	wg           sync.WaitGroup
	shutdown     chan interface{}
}

func (cw *CoinserverWatcher) Stop() {
	// Trigger the stopping of the watcher, and wait for complete shutdown (it
	// will close channel 'done' on exit)
	if cw.shutdown == nil {
		return
	}
	close(cw.shutdown)
	cw.wg.Wait()
}

func (cw *CoinserverWatcher) Start() {
	cw.wg = sync.WaitGroup{}
	cw.shutdown = make(chan interface{})
	go cw.RunTemplateBroadcaster()
	go cw.RunBlockCastListener()
}

func (cw *CoinserverWatcher) RunBlockCastListener() {
	cw.wg.Add(1)
	defer cw.wg.Done()

	connCfg := &rpcclient.ConnConfig{
		Host:         cw.endpoint[7:] + "rpc",
		User:         "",
		Pass:         "",
		HTTPPostMode: true, // Bitcoin core only supports HTTP POST mode
		DisableTLS:   true, // Bitcoin core does not provide TLS by default
	}
	client, err := rpcclient.New(connCfg, nil)
	if err != nil {
		panic(err)
	}

	listener := make(chan interface{})
	cw.blockCast.Register(listener)
	defer func() {
		log.Debug("Closing template listener channel")
		cw.blockCast.Unregister(listener)
		close(listener)
	}()
	for {
		msg := <-listener
		newBlock := msg.([]byte)
		if err != nil {
			log.WithError(err).Error("Invalid type recieved from blockCast")
			continue
		}
		blk, err := btcutil.NewBlockFromBytes(newBlock)
		if err != nil {
			log.WithError(err).Info("Error generating block")
			continue
		}
		res := client.SubmitBlock(blk, &btcjson.SubmitBlockOptions{})
		if err != nil {
			log.WithError(err).Info("Error submitting block")
		} else if res == nil {
			log.Info("Found a block!")
		} else if res.Error() == "inconclusive" {
			log.Info("Found a block! (inconclusive)")
		} else {
			log.Info("Maybe found a block: ", res)
		}
	}
}

func (cw *CoinserverWatcher) RunTemplateBroadcaster() {
	cw.wg.Add(1)
	defer cw.wg.Done()
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
				log.WithError(err).Warnf(
					"CoinserverWatcher %s:%+v is now DOWN", cw.id, cw.tmplKey)
			}
			cw.status = "down"
			select {
			case <-cw.shutdown:
				return
			case <-time.After(time.Second * 2):
			}
			continue
		}
		lastEvent := sse.Event{}
		cw.status = "up"
		log.Infof(
			"CoinserverWatcher %s:%+v is now UP", cw.id, cw.tmplKey)
		for {
			// Wait for new event or exit signal
			select {
			case <-cw.shutdown:
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
						log.Infof(
							"CoinserverWatcher %s:%+v is now LIVE", cw.id, cw.tmplKey)
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
			tmplKey := TemplateKey{
				Currency:     labels["currency"].(string),
				Algo:         labels["algo"].(string),
				TemplateType: labels["template_type"].(string),
			}
			currencyCast := n.getTemplateCast(tmplKey)
			blockCast := n.getBlockCast(labels["currency"].(string))
			cw := &CoinserverWatcher{
				endpoint:     labels["endpoint"].(string),
				status:       "starting",
				currencyCast: currencyCast,
				blockCast:    blockCast,
				id:           update.ServiceID,
				tmplKey:      tmplKey,
			}
			n.coinserverWatchers[update.ServiceID] = cw
			cw.Start()
			log.Infof("New coinserver detected: %s %+v", update.ServiceID, update.Status)
		default:
			log.Warn("Unrecognized action from service watcher ", update.Action)
		}
	}
}

func (n *Ngpool) getTemplateCast(key TemplateKey) broadcast.Broadcaster {
	n.templateCastMtx.Lock()
	if _, ok := n.templateCast[key]; !ok {
		n.templateCast[key] = broadcast.NewBroadcaster(10)
	}
	n.templateCastMtx.Unlock()
	return n.templateCast[key]
}

func (n *Ngpool) getBlockCast(key string) broadcast.Broadcaster {
	n.blockCastMtx.Lock()
	if _, ok := n.blockCast[key]; !ok {
		n.blockCast[key] = broadcast.NewBroadcaster(10)
	}
	n.blockCastMtx.Unlock()
	return n.blockCast[key]
}
