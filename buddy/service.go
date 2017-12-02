package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/coreos/etcd/client"
	"strings"
	//	"github.com/satori/go.uuid.git"
	"github.com/sergi/go-diff/diffmatchpatch"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Service struct {
	config        *viper.Viper
	serviceID     string
	namespace     string
	getAttributes func() map[string]interface{}
	pushMeta      chan map[string]interface{}
	etcd          client.Client
	etcdKeys      client.KeysAPI
}

func NewService(namespace string, config *viper.Viper, getAttributes func() map[string]interface{}) *Service {
	s := &Service{
		namespace:     namespace,
		config:        config,
		getAttributes: getAttributes,
		serviceID:     config.GetString("ServiceID"),
	}
	s.config.SetDefault("EtcdEndpoint", []string{"http://127.0.0.1:2379", "http://127.0.0.1:4001"})

	log.Infof("Loaded service ID %s, pulling config from etcd", s.serviceID)
	keyPath := "/config/" + s.namespace + "/" + s.serviceID
	s.config.AddRemoteProvider("etcd", s.config.GetStringSlice("EtcdEndpoint")[0], keyPath)
	s.config.SetConfigType("yaml")
	err := s.config.ReadRemoteConfig()
	if err != nil {
		log.WithError(err).WithField("keypath", keyPath).Warn("Unable to load from etcd")
	}

	cfg := client.Config{
		Endpoints: s.config.GetStringSlice("EtcdEndpoint"),
		Transport: client.DefaultTransport,
		// set timeout per request to fail fast when the target endpoint is unavailable
		HeaderTimeoutPerRequest: time.Second,
	}
	etcd, err := client.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	s.etcd = etcd
	s.etcdKeys = client.NewKeysAPI(s.etcd)

	return s
}

func (s *Service) KeepAlive() error {
	var (
		lastStatus string
		lastMeta   map[string]interface{} = make(map[string]interface{})
		serviceID  string                 = s.config.GetString("ServiceID")
	)
	for {
		select {
		case lastMeta = <-s.pushMeta:
		case <-time.After(time.Second * 10):
		}
		time.Sleep(time.Second * 10)
		statusMap := s.getAttributes()
		statusMap["meta"] = lastMeta
		statusRaw, err := json.Marshal(statusMap)
		status := string(statusRaw)
		if err != nil {
			log.WithError(err).Error("Failed serialization of status update")
			continue
		}
		opt := &client.SetOptions{
			TTL: time.Second * 15,
		}
		// Don't update if no new information, just refresh TTL
		if status == lastStatus {
			opt.Refresh = true
			opt.PrevExist = client.PrevExist
			status = ""
		} else {
			lastStatus = status
		}
		_, err = s.etcdKeys.Set(
			context.Background(), "/services/"+s.namespace+"/"+serviceID, status, opt)
		if err != nil {
			log.WithError(err).Warn("Failed to update etcd status entry")
			continue
		}
	}
	return nil
}

func (s *Service) SetupCmds(rootCmd *cobra.Command) {
	var (
		fileName string
	)
	editconfig := &cobra.Command{
		Use:   "editconfig",
		Short: "Opens the config in an editor",
		Run: func(cmd *cobra.Command, args []string) {
			configKeyPath := "/config/" + s.namespace + "/" + s.serviceID
			log.Info(configKeyPath)
			editor := "vim"

			// Get current config
			configResp, err := s.etcdKeys.Get(context.Background(), configKeyPath, nil)
			var currentConfig string = ""
			if err != nil {
				log.WithError(err).Info("Failed fetching config")
				reader := bufio.NewReader(os.Stdin)
				fmt.Print("Load default config? (y,n,q) ")
				text, _ := reader.ReadString('\n')
				input := strings.TrimSpace(text)
				if input == "y" {
					b, err := yaml.Marshal(s.config.AllSettings())
					log.Info(string(b))
					if err != nil {
						log.WithError(err).Fatal("Failed to serialize config")
					}
					currentConfig = string(b)
				} else if input == "q" {
					return
				}
			} else {
				currentConfig = string(configResp.Node.Value)
			}

			// Generate a new temporary file with our config from the server
			randSuffix := time.Now().UnixNano() + int64(os.Getpid())
			fname := fmt.Sprintf("%s_cfgscratch.%d.yaml", s.namespace, randSuffix)
			fpath := filepath.Join(os.TempDir(), fname)
			tmpFile, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
			if os.IsExist(err) {
				log.WithError(err).WithField("fname", fname).Fatal("Failed to make file, maybe another editor is open now?")
			}
			_, err = tmpFile.WriteString(currentConfig)
			if err != nil {
				log.WithError(err).Fatal("Failed to write tmp file")
			}
			tmpFile.Close()
			defer os.Remove(fpath)

			// Launch editor with our tmp file
			editorPath, err := exec.LookPath(editor)
			if err != nil {
				log.WithError(err).Fatalf("Failed to lookup path for '%s'", editor)
			}
			editFile := func() string {
				cmd := exec.Command(editorPath, tmpFile.Name())
				cmd.Stdin = os.Stdin
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				err = cmd.Start()
				if err != nil {
					log.WithError(err).Fatal("Failed to start editor")
				}
				err = cmd.Wait()

				// Read in edited config
				newConfigByte, err := ioutil.ReadFile(fpath)
				newConfig := string(newConfigByte)
				if err != nil {
					log.WithError(err).Fatal("Somehow we fail to read a file we just made...")
				}
				dmp := diffmatchpatch.New()
				diffs := dmp.DiffMain(currentConfig, newConfig, false)
				fmt.Println(dmp.DiffPrettyText(diffs))
				return newConfig
			}

			for {
				newConfig := editFile()
				for {
					reader := bufio.NewReader(os.Stdin)
					fmt.Print("Push changes (y,n,e): ")
					text, _ := reader.ReadString('\n')
					input := strings.TrimSpace(text)
					if input == "y" {
						_, err = s.etcdKeys.Set(
							context.Background(), configKeyPath, newConfig, nil)
						if err != nil {
							log.WithError(err).Fatal("Failed pushing config")
						}
						log.Infof("Successfully pushed to /config/%s/%s", s.namespace, s.serviceID)
						return
					} else if input == "e" {
						break
					} else if input != "n" {
						continue
					}
					return
				}
			}
		}}
	loadconfigCmd := &cobra.Command{
		Use:   "pushconfig",
		Short: "Loads the config and displays it",
		Run: func(cmd *cobra.Command, args []string) {
			fileInput, err := ioutil.ReadFile(fileName)
			serviceID := s.config.GetString("ServiceID")
			if serviceID == "" {
				log.Fatal("Cannot push config to etcd without a ServiceID (hint: export SERVICEID=veryuniquestring")
			}
			_, err = s.etcdKeys.Set(
				context.Background(), "/config/"+s.namespace+"/"+serviceID, string(fileInput), nil)
			if err != nil {
				log.WithError(err).Fatal("Failed pushing config")
			}
			log.Infof("Successfully pushed '%s' to /config/%s/%s", fileName, s.namespace, serviceID)
		}}
	loadconfigCmd.Flags().StringVarP(&fileName, "config", "c", "", "the config to load")
	dumpconfigCmd := &cobra.Command{
		Use:   "dumpconfig",
		Short: "Loads the config and displays it",
		Run: func(cmd *cobra.Command, args []string) {
			b, err := yaml.Marshal(s.config.AllSettings())
			if err != nil {
				fmt.Println("error:", err)
			}
			fmt.Println(string(b))
		}}
	RootCmd.AddCommand(editconfig)
	RootCmd.AddCommand(dumpconfigCmd)
	RootCmd.AddCommand(loadconfigCmd)
}
