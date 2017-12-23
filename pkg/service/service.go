package service

import (
	"context"
	"encoding/json"
	"github.com/coreos/etcd/client"
	log "github.com/inconshreveable/log15"
	"github.com/spf13/viper"
	_ "github.com/spf13/viper/remote"
	"os"
	"strings"
	"time"
)

type Service struct {
	config     *viper.Viper
	serviceID  string
	namespace  string
	pushStatus chan map[string]interface{}
	etcd       client.Client
	etcdKeys   client.KeysAPI
}

type ServiceStatusUpdate struct {
	ServiceType string
	ServiceID   string
	Status      *ServiceStatus
	Action      string
}

type ServiceStatus struct {
	ServiceID  string
	Status     map[string]interface{}
	Labels     map[string]interface{}
	UpdateTime time.Time
}

func NewService(namespace string, config *viper.Viper) *Service {
	s := &Service{
		namespace: namespace,
		config:    config,
	}
	s.serviceID = s.config.GetString("ServiceID")
	s.config.SetDefault("EtcdEndpoint", []string{"http://127.0.0.1:2379", "http://127.0.0.1:4001"})

	log.Info("Loaded service, pulling config from etcd", "service", s.serviceID)
	s.config.SetConfigType("yaml")

	keyPath := "/config/" + s.namespace + "/" + s.serviceID
	s.config.AddRemoteProvider("etcd", s.config.GetStringSlice("EtcdEndpoint")[0], keyPath)
	err := s.config.ReadRemoteConfig()
	if err != nil {
		log.Warn("Unable to load from etcd", "err", err, "keypath", keyPath)
	}

	cfg := client.Config{
		Endpoints: s.config.GetStringSlice("EtcdEndpoint"),
		Transport: client.DefaultTransport,
		// set timeout per request to fail fast when the target endpoint is unavailable
		HeaderTimeoutPerRequest: time.Second,
	}
	etcd, err := client.New(cfg)
	if err != nil {
		log.Crit("Failed to make etcd client", "err", err)
		os.Exit(1)
	}
	s.etcd = etcd
	s.etcdKeys = client.NewKeysAPI(s.etcd)

	res, err := s.etcdKeys.Get(context.Background(), "/config/common", nil)
	if err != nil {
		log.Crit("Unable to contact etcd", "err", err)
		os.Exit(1)
	}
	s.config.MergeConfig(strings.NewReader(res.Node.Value))

	s.SetupCurrencies()
	s.SetupShareChains()
	return s
}

func (s *Service) parseNode(node *client.Node) (string, *ServiceStatus) {
	// Parse all the node details about the watcher
	lbi := strings.LastIndexByte(node.Key, '/') + 1
	serviceID := node.Key[lbi:]
	var status ServiceStatus
	json.Unmarshal([]byte(node.Value), &status)
	status.ServiceID = serviceID
	return serviceID, &status
}

// Requests all services of a specific namespace. This is used in the same
// context as ServiceWatcher, except for simple script executions
func (s *Service) LoadServices(namespace string) (map[string]*ServiceStatus, error) {
	statuses, _, err := s.loadServices(namespace)
	return statuses, err
}

func (s *Service) loadServices(namespace string) (map[string]*ServiceStatus, uint64, error) {
	var services map[string]*ServiceStatus = make(map[string]*ServiceStatus)
	var watchStatusKeypath string = "/status/" + namespace

	getOpt := &client.GetOptions{
		Recursive: true,
	}
	res, err := s.etcdKeys.Get(context.Background(), watchStatusKeypath, getOpt)
	// If service key doesn't exist, create it so watcher can start
	if cerr, ok := err.(client.Error); ok && cerr.Code == client.ErrorCodeKeyNotFound {
		log.Info("Creating empty dir in etcd", "dir", watchStatusKeypath)
		_, err := s.etcdKeys.Set(context.Background(), watchStatusKeypath,
			"", &client.SetOptions{Dir: true})
		if err != nil {
			return nil, 0, err
		}
	} else if err != nil {
		return nil, 0, err
	} else {
		for _, node := range res.Node.Nodes {
			serviceID, serviceStatus := s.parseNode(node)
			services[serviceID] = serviceStatus
		}
	}
	return services, res.Index, nil
}

// This watches for services of a specific namespace to change, and broadcasts
// those changes over the provided channel. How the updates are handled is up
// to the reciever
func (s *Service) ServiceWatcher(watchNamespace string) (chan ServiceStatusUpdate, error) {
	var (
		services           map[string]*ServiceStatus = make(map[string]*ServiceStatus)
		watchStatusKeypath string                    = "/status/" + watchNamespace
		// We assume you have no more than 1000 services... Sloppy!
		updates chan ServiceStatusUpdate = make(chan ServiceStatusUpdate, 1000)
	)

	services, startIndex, err := s.loadServices(watchNamespace)
	if err != nil {
		return nil, err
	}
	for _, svc := range services {
		services[svc.ServiceID] = svc
		updates <- ServiceStatusUpdate{
			ServiceType: watchNamespace,
			ServiceID:   svc.ServiceID,
			Status:      svc,
			Action:      "added",
		}
	}

	// Start a watcher for all changes after the pull we're doing
	watchOpt := &client.WatcherOptions{
		AfterIndex: startIndex,
		Recursive:  true,
	}
	watcher := s.etcdKeys.Watcher(watchStatusKeypath, watchOpt)
	go func() {
		for {
			res, err := watcher.Next(context.Background())
			if err != nil {
				log.Warn("Error from coinserver watcher", "err", err)
				time.Sleep(time.Second * 2)
				continue
			}
			serviceID, serviceStatus := s.parseNode(res.Node)
			if serviceStatus == nil {
			}
			_, exists := services[serviceID]
			var action string
			if res.Action == "expire" {
				if exists {
					delete(services, serviceID)
					// Service status from the etcd notification will be nil,
					// so pull it
					serviceStatus = services[serviceID]
					action = "removed"
				}
			} else if res.Action == "set" || res.Action == "update" {
				services[serviceID] = serviceStatus
				// NOTE: Will fire event even when no change is actually made.
				// Shouldn't happen, but might.
				if exists {
					action = "updated"
				} else {
					action = "added"
				}
			} else {
				log.Debug("Ignoring watch update type ", res.Action)
			}

			// A little sloppy, but more DRY
			if action != "" {
				log.Debug("Broadcasting service update", "action", action, "id", serviceID)
				updates <- ServiceStatusUpdate{
					ServiceType: watchNamespace,
					ServiceID:   serviceID,
					Status:      serviceStatus,
					Action:      action,
				}
			}
		}
	}()
	return updates, nil
}

func (s *Service) KeepAlive(labels map[string]interface{}) error {
	var (
		lastValue  string
		lastStatus map[string]interface{} = make(map[string]interface{})
		serviceID  string                 = s.config.GetString("ServiceID")
	)
	if len(labels) == 0 {
		log.Crit("Cannot start service KeepAlive without labels")
		os.Exit(1)
	}
	for {
		select {
		case lastStatus = <-s.pushStatus:
		case <-time.After(time.Second * 1):
		}

		// Serialize a new value to write
		valueMap := map[string]interface{}{}
		valueMap["labels"] = labels
		valueMap["status"] = lastStatus
		valueRaw, err := json.Marshal(valueMap)
		value := string(valueRaw)
		if err != nil {
			log.Error("Failed serialization of status update", "err", err)
			continue
		}

		opt := &client.SetOptions{TTL: time.Second * 2}
		// Don't update if no new information, just refresh TTL
		if value == lastValue {
			opt.Refresh = true
			opt.PrevExist = client.PrevExist
			value = ""
		} else {
			lastValue = value
		}

		// Set TTL update, or new information
		_, err = s.etcdKeys.Set(
			context.Background(), "/status/"+s.namespace+"/"+serviceID, value, opt)
		if err != nil {
			log.Warn("Failed to update etcd status entry", "err", err)
			continue
		}
	}
	return nil
}
