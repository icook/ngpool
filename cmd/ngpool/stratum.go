package main

import (
	"encoding/binary"
	"encoding/json"
	"github.com/dustin/go-broadcast"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
	"github.com/seehuhn/sha256d"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"math/big"
	"sync"
	"time"
)

type StratumServer struct {
	config          *viper.Viper
	getTemplateCast func(TemplateKey) broadcast.Broadcaster
	jobCast         broadcast.Broadcaster
}

func NewStratumServer(config *viper.Viper, getTemplateCast func(TemplateKey) broadcast.Broadcaster) *StratumServer {
	ss := &StratumServer{
		config:          config,
		getTemplateCast: getTemplateCast,
		jobCast:         broadcast.NewBroadcaster(10),
	}
	return ss
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

func (s *StratumServer) Miner() {
	listener := make(chan interface{})
	s.jobCast.Register(listener)
	jobLock := sync.Mutex{}
	var job *Job

	// Watch for new jobs for us
	go func() {
		for {
			jobOrig := <-listener
			newJob, ok := jobOrig.(*Job)
			if job == nil || !ok {
				log.WithField("job", jobOrig).Warn("Bad job from broadcast")
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

			var hasher = sha256d.New()
			hasher.Write(header)
			blockHash.SetBytes(hasher.Sum(nil))

			if blockHash.Cmp(job.target) == -1 {
				block := job.getBlock(header, coinbase)
				log.Infof("Found a block! \n%x", block)
				return
			}
			jobLock.Unlock()
			i += 1
		}
	}()
}

func (s *StratumServer) Start() {
	latestTemp := map[TemplateKey][]byte{}
	latestTempMtx := sync.Mutex{}

	vals := s.config.Get("Currencies")
	for _, val := range vals.([]interface{}) {
		var tmplKey TemplateKey
		err := mapstructure.Decode(val, &tmplKey)
		if err != nil {
			log.WithError(err).Error("Invalid configuration, currencies of improper format")
			return
		}
		go func() {
			log.Infof("Registering listener for %+v", tmplKey)
			listener := make(chan interface{})
			broadcast := s.getTemplateCast(tmplKey)
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
				s.jobCast.Submit(job)
				latestTempMtx.Unlock()
			}
		}()
	}

	go s.Miner()
}
