package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/dustin/go-broadcast"
	"github.com/icook/btcd/rpcclient"
	log "github.com/inconshreveable/log15"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	_ "github.com/lib/pq"
	"github.com/mitchellh/mapstructure"
	"github.com/r3labs/sse"
	"github.com/seehuhn/sha256d"
	"github.com/spf13/viper"

	"github.com/icook/ngpool/pkg/lbroadcast"
	"github.com/icook/ngpool/pkg/service"
)

type BlockSolve struct {
	powhash        *big.Int
	target         *big.Int
	coinbaseHash   []byte
	height         int64
	subsidy        int64
	powalgo        string
	data           []byte
	subsidyAddress string
}

func (b *BlockSolve) getBlockHash() string {
	var hasher = sha256d.New()
	hasher.Write(b.data[:80])
	ret := hasher.Sum(nil)
	return hex.EncodeToString(ret)
}

type Share struct {
	username   string
	time       time.Time
	difficulty float64
	currencies []string
	blocks     map[string]*BlockSolve
}

type Template struct {
	key  TemplateKey
	data []byte
}

type TemplateKey struct {
	Algo         string
	Currency     string
	TemplateType string
}

type StratumServer struct {
	config     *viper.Viper
	tmplKeys   []TemplateKey
	db         *sqlx.DB
	shareChain *service.ShareChainConfig

	coinserverWatchers map[string]*CoinserverWatcher
	newShare           chan *Share
	newTemplate        chan *Template
	newClient          chan *StratumClient
	jobCast            broadcast.Broadcaster
	service            *service.Service

	lastJob    *Job
	lastJobMtx *sync.Mutex

	// Keyed by currency code
	blockCast    map[string]broadcast.Broadcaster
	blockCastMtx *sync.Mutex
}

func NewStratumServer() *StratumServer {
	ng := &StratumServer{
		newTemplate:  make(chan *Template),
		newShare:     make(chan *Share),
		newClient:    make(chan *StratumClient),
		blockCast:    make(map[string]broadcast.Broadcaster),
		blockCastMtx: &sync.Mutex{},
		lastJobMtx:   &sync.Mutex{},
		jobCast:      lbroadcast.NewLastBroadcaster(10),
	}
	return ng
}

func (n *StratumServer) ConfigureService(name string, etcdEndpoints []string) {
	n.service = service.NewService("stratum", etcdEndpoints)
	n.config = n.service.LoadCommonConfig()
	n.service.LoadServiceConfig(n.config, name)
}

func (n *StratumServer) ParseConfig() {
	n.config.SetDefault("LogLevel", "info")
	n.config.SetDefault("EnableCpuminer", false)
	n.config.SetDefault("StratumBind", "127.0.0.1:3333")

	scn := n.config.GetString("ShareChainName")
	sc, ok := service.ShareChain[scn]
	if !ok {
		keys := []string{}
		for key, _ := range service.ShareChain {
			keys = append(keys, key)
		}
		log.Crit("Invalid ShareChainName", "options", keys, "setting", scn)
		os.Exit(1)
	}
	n.shareChain = sc

	db, err := sqlx.Connect("postgres", n.config.GetString("DbConnectionString"))
	if err != nil {
		log.Crit("Failed to connect to db", "err", err)
		os.Exit(1)
	}
	n.db = db

	levelConfig := n.config.GetString("LogLevel")
	level, err := log.LvlFromString(levelConfig)
	if err != nil {
		log.Crit("Unable to parse log level", "configval", levelConfig, "err", err)
		os.Exit(1)
	}
	handler := log.CallerFileHandler(log.StdoutHandler)
	handler = log.LvlFilterHandler(level, handler)
	log.Root().SetHandler(handler)
	log.Info("Set log level", "level", level)

	var tmplKeys []TemplateKey
	val := n.config.Get("AuxCurrencies")
	err = mapstructure.Decode(val, &tmplKeys)
	if err != nil {
		log.Error("Invalid configuration, 'AuxCurrency' of improper format", "err", err)
		return
	}

	var tmplKey TemplateKey
	val = n.config.Get("BaseCurrency")
	err = mapstructure.Decode(val, &tmplKey)
	if err != nil {
		log.Error("Invalid configuration, 'BaseCurrency' of improper format", "err", err)
		return
	}
	n.tmplKeys = append(tmplKeys, tmplKey)
}

func (n *StratumServer) Start() {
	go n.listenTemplates()

	updates, err := n.service.ServiceWatcher("coinserver")
	if err != nil {
		log.Crit("Failed to start coinserver watcher", "err", err)
		os.Exit(1)
	}
	go n.HandleCoinserverWatcherUpdates(updates)
	go n.service.KeepAlive(map[string]string{
		"endpoint": n.config.GetString("StratumBind"),
	})

	if n.config.GetBool("EnableCpuminer") {
		go n.Miner()
	}
	go n.ListenMiners()
	go n.ListenShares()
	go n.UpdateStatus()
}

func (n *StratumServer) UpdateStatus() {
	var clients = map[string]*StratumClient{}
	var ticker = time.NewTicker(time.Second * 1)
	for {
		select {
		case newClient := <-n.newClient:
			clients[newClient.id] = newClient
		case <-ticker.C:
			var clientStatuses = []interface{}{}
			for _, client := range clients {
				if client.hasShutdown {
					delete(clients, client.id)
					continue
				}
				clientStatuses = append(clientStatuses, client.status())
			}
			n.service.PushStatus <- map[string]interface{}{
				"clients": clientStatuses,
			}
		}
	}
}

func (n *StratumServer) ListenShares() {
	log.Debug("Starting ListenShares")
	for {
		share := <-n.newShare
		log.Debug("Got share", "share", share)

		// Fire off submissions for all blocks first, before touching SQL
		for currencyCode, block := range share.blocks {
			n.blockCast[currencyCode].Submit(block)
		}

		// Insert a block and UTXO (the coinbase) for each solve
		for currencyCode, block := range share.blocks {
			_, err := n.db.Exec(
				`INSERT INTO utxo (hash, vout, amount, currency, address)
				VALUES ($1, $2, $3, $4, $5)`,
				hex.EncodeToString(block.coinbaseHash),
				0, // Coinbase UTXO is always first and only UTXO
				block.subsidy,
				currencyCode,
				block.subsidyAddress)
			if err != nil {
				log.Error("Failed to save block UTXO", "err", err)
			}

			_, err = n.db.Exec(
				`INSERT INTO block
				(height, currency, powalgo, hash, powhash, subsidy, mined_at,
					mined_by, target, coinbase_hash)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				block.height,
				currencyCode,
				block.powalgo,
				block.getBlockHash(),
				hex.EncodeToString(block.powhash.Bytes()),
				block.subsidy,
				share.time,
				share.username,
				block.target.String(),
				hex.EncodeToString(block.coinbaseHash))
			if err != nil {
				log.Error("Failed to save block", "err", err)
			}
		}

		// Log the users share
		_, err := n.db.Exec(
			`INSERT INTO share (username, difficulty, mined_at, sharechain, currencies)
			VALUES ($1, $2, $3, $4, $5)`,
			share.username,
			share.difficulty,
			share.time,
			n.shareChain.Name,
			pq.StringArray(share.currencies))
		if err != nil {
			log.Error("Failed to save share", "err", err)
		}
	}
}

func (n *StratumServer) listenTemplates() {
	// Starts a goroutine to listen for new templates from newTemplate channel.
	// When new templates are available a new job is created and broadcasted
	// over jobBroadcast
	latestTemp := map[TemplateKey][]byte{}
	var lastJobFlush interface{}
	for {
		newTemplate := <-n.newTemplate
		log.Info("Got new template", "key", newTemplate.key)
		latestTemp[newTemplate.key] = newTemplate.data
		job, err := NewJobFromTemplates(latestTemp)
		ignore, lastJobFlush := job.SetFlush(lastJobFlush)
		if err != nil {
			log.Error("Error generating job", "err", err)
			continue
		}
		if ignore {
			log.Info("Ignoring stale job")
			continue
		}
		n.lastJobMtx.Lock()
		n.lastJob = job
		n.lastJobMtx.Unlock()
		n.jobCast.Submit(job)
		log.Info("New job pushed", "lastJobFlush", lastJobFlush)
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

			solves, _, _, err := job.CheckSolves(nonce, extraNonceMagic, nil)
			if err != nil {
				log.Warn("Failed to check solves for job", "err", err)
			}
			for currencyCode, block := range solves {
				n.blockCast[currencyCode].Submit(block)
			}
			if len(solves) > 0 {
				time.Sleep(time.Second * 10)
			}
			jobLock.Unlock()
			i += 1
		}
	}()
}

func (n *StratumServer) Stop() {
}

type CoinserverWatcher struct {
	id          string
	tmplKey     TemplateKey
	endpoint    string
	status      string
	newTemplate chan *Template
	blockCast   broadcast.Broadcaster
	wg          sync.WaitGroup
	shutdown    chan interface{}
	log         log.Logger
}

func (cw *CoinserverWatcher) Stop() {
	// Trigger the stopping of the watcher, and wait for complete shutdown (it
	// will close channel 'done' on exit)
	if cw.shutdown == nil {
		return
	}
	close(cw.shutdown)
	cw.wg.Wait()
	cw.log.Info("CoinserverWatcher shutdown complete")
}

func (cw *CoinserverWatcher) Start() {
	cw.log = log.New("coin", cw.tmplKey.Currency, "id", cw.id)
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
		cw.log.Debug("Closing template listener channel")
		cw.blockCast.Unregister(listener)
		close(listener)
	}()
	for {
		select {
		case <-cw.shutdown:
			return
		case msg := <-listener:
			newBlock := msg.(*BlockSolve)
			if err != nil {
				cw.log.Error("Invalid type recieved from blockCast", "err", err)
				continue
			}
			hexString := hex.EncodeToString(newBlock.data)
			encodedBlock, err := json.Marshal(hexString)
			if err != nil {
				cw.log.Error("Failed to json marshal a string", "err", err)
				continue
			}
			params := []json.RawMessage{
				encodedBlock,
				[]byte{'[', ']'},
			}
			res, err := client.RawRequest("submitblock", params)
			if err != nil {
				cw.log.Info("Error submitting block", "err", err)
			} else {
				cw.log.Info("Submitted block", "result", string(res), "height", newBlock.height)
			}
		}
	}
}

func (cw *CoinserverWatcher) RunTemplateBroadcaster() {
	cw.wg.Add(1)
	defer cw.wg.Done()
	logger := log.New("id", cw.id, "tmplKey", cw.tmplKey)
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
				logger.Warn("CoinserverWatcher is now DOWN", "err", err)
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
		logger.Debug("CoinserverWatcher is now UP")
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
						logger.Error("Bad payload from coinserver", "payload", decoded)
					}
					lastEvent.Data = decoded
					logger.Debug("Got new template", "data", string(decoded))
					cw.newTemplate <- &Template{
						data: lastEvent.Data,
						key:  cw.tmplKey,
					}
					if cw.status != "live" {
						logger.Info("CoinserverWatcher is now LIVE")
					}
					cw.status = "live"
				}
			}
		}
	}
}

func (n *StratumServer) ListenMiners() {
	endpoint := n.config.GetString("StratumBind")
	listener, err := net.Listen("tcp", endpoint)
	if err != nil {
		log.Crit("Failed to listen stratum", "err", err)
		os.Exit(1)
	}
	log.Info("Listening stratum", "endpoint", endpoint)
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Warn("Failed to accept connection", "err", err)
			continue
		}
		client := NewClient(conn, n.jobCast, n.newShare)
		client.Start()
		n.newClient <- client
	}
}

func (n *StratumServer) HandleCoinserverWatcherUpdates(
	updates chan service.ServiceStatusUpdate) {
	coinserverWatchers := map[string]*CoinserverWatcher{}
	log.Info("Listening for new coinserver services")
	for {
		update := <-updates
		switch update.Action {
		case "removed":
			if csw, ok := coinserverWatchers[update.ServiceID]; ok {
				log.Info("Coinserver shutdown", "id", update.ServiceID)
				go csw.Stop()
			}
		case "updated":
			log.Debug("Coinserver status update", "id", update.ServiceID, "new_status", update.Status)
		case "added":
			labels := update.Status.Labels
			// TODO: Should probably serialize to datatype...
			tmplKey := TemplateKey{
				Currency:     labels["currency"],
				Algo:         labels["algo"],
				TemplateType: labels["template_type"],
			}
			// I'm sure there's a less verbose way to do this. If we're not
			// interested in the templates of this coinserver, ignore the
			// update and continue
			found := false
			for _, key := range n.tmplKeys {
				if key == tmplKey {
					found = true
					break
				}
			}
			if !found {
				log.Debug("Ignoring coinserver", "id", update.ServiceID, "key", tmplKey)
				continue
			}

			// Create a watcher service that listens for block submission on
			// blockCast and pushes new templates to newTemplate channel
			cw := n.NewCoinserverWatcher(
				labels["endpoint"], update.ServiceID, tmplKey)
			coinserverWatchers[update.ServiceID] = cw
			cw.Start()
			log.Debug("New coinserver detected", "id", update.ServiceID, "tmplKey", tmplKey)
		default:
			log.Warn("Unrecognized action from service watcher", "action", update.Action)
		}
	}
}

func (n *StratumServer) NewCoinserverWatcher(endpoint string, name string,
	tmplKey TemplateKey) *CoinserverWatcher {
	blockCast := n.getBlockCast(tmplKey.Currency)
	cw := &CoinserverWatcher{
		endpoint:    endpoint,
		status:      "starting",
		newTemplate: n.newTemplate,
		blockCast:   blockCast,
		id:          name,
		tmplKey:     tmplKey,
	}
	return cw
}

func (n *StratumServer) getBlockCast(key string) broadcast.Broadcaster {
	n.blockCastMtx.Lock()
	if _, ok := n.blockCast[key]; !ok {
		n.blockCast[key] = broadcast.NewBroadcaster(10)
	}
	n.blockCastMtx.Unlock()
	return n.blockCast[key]
}
