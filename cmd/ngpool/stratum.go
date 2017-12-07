package main

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
	"github.com/coreos/etcd/client"
	"github.com/dustin/go-broadcast"
	"github.com/icook/btcd/rpcclient"
	"github.com/icook/ngpool/pkg/service"
	log "github.com/inconshreveable/log15"
	"github.com/mitchellh/mapstructure"
	"github.com/r3labs/sse"
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

type StratumServer struct {
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

func NewStratumServer() *StratumServer {
	config := viper.New()

	config.SetDefault("LogLevel", "info")
	config.SetDefault("Ports", []string{})
	// Load from Env so we can access etcd
	config.AutomaticEnv()

	ng := &StratumServer{
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

func (n *StratumServer) Start(service *service.Service) {
	levelConfig := n.config.GetString("LogLevel")
	level, err := log.LvlFromString(levelConfig)
	if err != nil {
		log.Crit("Unable to parse log level", "configval", levelConfig, "err", err)
	}
	log.Info("Set log level", "level", level)
	handler := log.LvlFilterHandler(level, log.Root().GetHandler())
	log.Root().SetHandler(handler)

	updates, err := service.ServiceWatcher("coinserver")
	if err != nil {
		log.Crit("Failed to start coinserver watcher", "err", err)
	}

	go n.HandleCoinserverWatcherUpdates(updates)

	var tmplKey TemplateKey
	val := n.config.Get("BaseCurrency")
	err = mapstructure.Decode(val, &tmplKey)
	if err != nil {
		log.Error("Invalid configuration, 'BaseCurrency' of improper format", "err", err)
		return
	}
	go n.listenTemplate(tmplKey)

	var tmplKeys []TemplateKey
	val = n.config.Get("AuxCurrencies")
	err = mapstructure.Decode(val, &tmplKeys)
	if err != nil {
		log.Error("Invalid configuration, 'AuxCurrency' of improper format", "err", err)
		return
	}
	for _, tmplKey := range tmplKeys {
		go n.listenTemplate(tmplKey)
	}

	go n.Miner()
}

func (n *StratumServer) listenTemplate(tmplKey TemplateKey) {
	logger := log.New("key", tmplKey)
	logger.Info("Registering template listener")
	listener := make(chan interface{})
	broadcast := n.getTemplateCast(tmplKey)
	broadcast.Register(listener)
	defer func() {
		logger.Debug("Closing template listener channel")
		broadcast.Unregister(listener)
		close(listener)
	}()
	for {
		newTemplate := <-listener
		logger.Info("Got new template")
		template, ok := newTemplate.([]byte)
		if !ok {
			logger.Error("Got invalid type from template listener", "template", newTemplate)
			continue
		}
		n.latestTempMtx.Lock()
		n.latestTemp[tmplKey] = template
		job, err := NewJobFromTemplates(n.latestTemp)
		if err != nil {
			logger.Error("Error generating job", "err", err)
			n.latestTempMtx.Unlock()
			continue
		}
		logger.Info("New job successfully generated, pushing...")
		n.jobCast.Submit(job)
		n.latestTempMtx.Unlock()
	}
}

func (n *StratumServer) Miner() {
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
				log.Warn("Bad job from broadcast", "job", jobOrig)
				continue
			}
			jobLock.Lock()
			job = newJob
			jobLock.Unlock()
		}
	}()
	go func() {
		var i uint32 = 0
		last := time.Now()
		lasti := i
		for {
			if i%10000 == 0 {
				t := time.Now()
				dur := t.Sub(last)
				if dur > (time.Second * 15) {
					hashrate := fmt.Sprintf("%.f hps", float64(i-lasti)/dur.Seconds())
					log.Info("Hashrate", "rate", hashrate)
					lasti = i
					last = t
				}
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
				log.Warn("Failed to check solves for job", "err", err)
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

func (n *StratumServer) Stop() {
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
			log.Error("Invalid type recieved from blockCast", "err", err)
			continue
		}
		blk, err := btcutil.NewBlockFromBytes(newBlock)
		if err != nil {
			log.Info("Error generating block", "err", err)
			continue
		}
		res := client.SubmitBlock(blk, &btcjson.SubmitBlockOptions{})
		if err != nil {
			log.Info("Error submitting block", "err", err)
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
				log.Warn("CoinserverWatcher is now DOWN",
					"id", cw.id, "tmplKey", cw.tmplKey, "err", err)
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
		log.Info("CoinserverWatcher is now UP", "id", cw.id, "tmplKey", cw.tmplKey)
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
						log.Warn("Bad payload from coinserver", "payload", decoded)
					}
					lastEvent.Data = decoded
					log.Debug("Got new template", "endpoint", cw.endpoint, "event", lastEvent)
					cw.currencyCast.Submit(lastEvent.Data)
					if cw.status != "live" {
						log.Info("CoinserverWatcher is now LIVE",
							"id", cw.id, "tmplKey", cw.tmplKey)
					}
					cw.status = "live"
				}
			}
		}
	}
}

func (n *StratumServer) HandleCoinserverWatcherUpdates(updates chan service.ServiceStatusUpdate) {
	log.Info("Listening for new coinserver services")
	for {
		update := <-updates
		switch update.Action {
		case "removed":
			if csw, ok := n.coinserverWatchers[update.ServiceID]; ok {
				log.Info("Coinserver shutdown", "id", update.ServiceID)
				csw.Stop()
			}
		case "updated":
			log.Info("Coinserver status update", "id", update.ServiceID, "new_status", update.Status)
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
			log.Info("New coinserver detected", "id", update.ServiceID, "tmplKey", tmplKey)
		default:
			log.Warn("Unrecognized action from service watcher", "action", update.Action)
		}
	}
}

func (n *StratumServer) getTemplateCast(key TemplateKey) broadcast.Broadcaster {
	n.templateCastMtx.Lock()
	if _, ok := n.templateCast[key]; !ok {
		n.templateCast[key] = broadcast.NewBroadcaster(10)
	}
	n.templateCastMtx.Unlock()
	return n.templateCast[key]
}

func (n *StratumServer) getBlockCast(key string) broadcast.Broadcaster {
	n.blockCastMtx.Lock()
	if _, ok := n.blockCast[key]; !ok {
		n.blockCast[key] = broadcast.NewBroadcaster(10)
	}
	n.blockCastMtx.Unlock()
	return n.blockCast[key]
}
