package main

import (
	"github.com/dustin/go-broadcast"
	"github.com/mitchellh/mapstructure"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"sync"
)

type StratumServer struct {
	config          *viper.Viper
	getTemplateCast func(TemplateKey) broadcast.Broadcaster
}

func NewStratumServer(config *viper.Viper, getTemplateCast func(TemplateKey) broadcast.Broadcaster) *StratumServer {
	ss := &StratumServer{
		config:          config,
		getTemplateCast: getTemplateCast,
	}
	return ss
}

func genJob(templates map[TemplateKey]string) {
	log.Info("I did something!")
}

func (s *StratumServer) Start() {
	latestTemp := map[TemplateKey]string{}
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
				template, ok := newTemplate.(string)
				if !ok {
					log.Errorf("Got invalid type from template listener: %#v", newTemplate)
					continue
				}
				latestTempMtx.Lock()
				latestTemp[tmplKey] = template
				genJob(latestTemp)
				latestTempMtx.Unlock()
			}
		}()
	}
}
