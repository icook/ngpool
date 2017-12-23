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
	name       string
	namespace  string
	pushStatus chan map[string]interface{}
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
	Labels     map[string]string
	UpdateTime time.Time
}

func NewService(namespace string, etcdEndpoints []string) *Service {
	cfg := client.Config{
		Endpoints: etcdEndpoints,
		Transport: client.DefaultTransport,
		// set timeout per request to fail fast when the target endpoint is unavailable
		HeaderTimeoutPerRequest: time.Second,
	}
	etcd, err := client.New(cfg)
	if err != nil {
		log.Crit("Failed to make etcd client", "err", err)
		os.Exit(1)
	}

	s := &Service{
		namespace: namespace,
		etcdKeys:  client.NewKeysAPI(etcd),
	}
	return s
}

func (s *Service) LoadServiceConfig(config *viper.Viper, name string) {
	s.name = name

	keyPath := "/config/" + s.namespace + "/" + s.name
	res, err := s.etcdKeys.Get(context.Background(), keyPath, nil)
	if err != nil {
		log.Crit("Unable to contact etcd", "err", err)
		os.Exit(1)
	}
	config.SetConfigType("yaml")
	config.MergeConfig(strings.NewReader(res.Node.Value))
}

func (s *Service) LoadCommonConfig() *viper.Viper {
	res, err := s.etcdKeys.Get(context.Background(), "/config/common", nil)
	if err != nil {
		log.Crit("Unable to contact etcd", "err", err)
		os.Exit(1)
	}
	config := viper.New()
	config.SetConfigType("yaml")
	config.MergeConfig(strings.NewReader(res.Node.Value))

	SetupCurrencies(config.GetStringMap("Currencies"))
	SetupShareChains(config.GetStringMap("ShareChains"))
	sub := config.Sub(s.namespace)
	if sub == nil {
		sub = viper.New()
	}
	return sub
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

func (s *Service) KeepAlive(labels map[string]string) error {
	var (
		lastValue  string
		lastStatus map[string]interface{} = make(map[string]interface{})
	)
	if s.name == "" {
		log.Crit(`Cannot start service KeepAlive without name set.
			Call LoadServiceConfig, or set manually`)
		os.Exit(1)
	}
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
			context.Background(), "/status/"+s.namespace+"/"+s.name, value, opt)
		if err != nil {
			log.Warn("Failed to update etcd status entry", "err", err)
			continue
		}
	}
	return nil
}
