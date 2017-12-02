package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/coreos/etcd/client"
	"github.com/fatih/color"
	"strings"
	//	"github.com/satori/go.uuid.git"
	"github.com/satori/go.uuid"
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
	configKeyPath string
	editor        string
}

func NewService(namespace string, config *viper.Viper, getAttributes func() map[string]interface{}) *Service {
	s := &Service{
		namespace:     namespace,
		config:        config,
		getAttributes: getAttributes,
		editor:        "vi",
	}
	s.SetServiceID(s.config.GetString("ServiceID"))
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

func (s *Service) SetServiceID(id string) {
	s.serviceID = id
	s.configKeyPath = "/config/" + s.namespace + "/" + s.serviceID
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

func (s *Service) getDefaultConfig() string {
	b, err := yaml.Marshal(s.config.AllSettings())
	if err != nil {
		log.WithError(err).Fatal("Failed to serialize config")
	}
	return string(b)
}

func (s *Service) editFile(fpath string) string {
	// Launch editor with our tmp file
	editorPath, err := exec.LookPath(s.editor)
	if err != nil {
		log.WithError(err).Fatalf("Failed to lookup path for '%s'", s.editor)
	}
	cmd := exec.Command(editorPath, fpath)
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
	return newConfig
}

func (s *Service) mktmp(contents string) *os.File {
	// Generate a new temporary file with our config from the server
	randSuffix := time.Now().UnixNano() + int64(os.Getpid())
	fname := fmt.Sprintf("%s_cfgscratch.%d.yaml", s.namespace, randSuffix)
	fpath := filepath.Join(os.TempDir(), fname)
	tmpFile, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		log.WithError(err).WithField("fname", fname).Fatal("Failed to make file, maybe another editor is open now?")
	}
	_, err = tmpFile.WriteString(contents)
	if err != nil {
		log.WithError(err).Fatal("Failed to write tmp file")
	}
	tmpFile.Close()
	return tmpFile
}

func (s *Service) modifyLoop(currentVal string) {
	tmpFile := s.mktmp(currentVal)
	defer os.Remove(tmpFile.Name())

	for {
		newConfig := s.editFile(tmpFile.Name())
		dmp := diffmatchpatch.New()
		diffs := dmp.DiffMain(currentVal, newConfig, false)
		fmt.Println(dmp.DiffPrettyText(diffs))
		for {
			reader := bufio.NewReader(os.Stdin)
			fmt.Print("Push changes (y,n,e): ")
			text, _ := reader.ReadString('\n')
			input := strings.TrimSpace(text)
			if input == "y" {
				_, err := s.etcdKeys.Set(
					context.Background(), s.configKeyPath, newConfig, nil)
				if err != nil {
					log.WithError(err).Fatal("Failed pushing config")
				}
				log.Infof("Successfully pushed to %s", s.configKeyPath)
				return
			} else if input == "e" {
				break
			} else if input != "n" {
				continue
			}
			return
		}
	}
}

func (s *Service) SetupCmds(rootCmd *cobra.Command) {
	editconfigCmd := &cobra.Command{
		Use:   "editconfig",
		Short: "Opens the config in an editor",
		Run: func(cmd *cobra.Command, args []string) {
			// Get current config
			configResp, err := s.etcdKeys.Get(context.Background(), s.configKeyPath, nil)
			var currentConfig string = ""
			if err != nil {
				log.WithError(err).Info("Failed fetching config")
				reader := bufio.NewReader(os.Stdin)
				fmt.Print("Load default config? (y,n,q) ")
				text, _ := reader.ReadString('\n')
				input := strings.TrimSpace(text)
				if input == "y" {
					currentConfig = s.getDefaultConfig()
				} else if input == "q" {
					return
				}
			} else {
				currentConfig = string(configResp.Node.Value)
			}

			s.modifyLoop(currentConfig)
		}}

	var fileName string
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

	newCmd := &cobra.Command{
		Use:   "new",
		Short: "Creates a new service configuration",
		Run: func(cmd *cobra.Command, args []string) {
			s.SetServiceID(uuid.NewV4().String())
			def := s.getDefaultConfig()
			s.modifyLoop(def)
		}}

	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "Lists all service configs",
		Run: func(cmd *cobra.Command, args []string) {
			getOpt := &client.GetOptions{
				Recursive: true,
			}
			res, err := s.etcdKeys.Get(context.Background(), "/config/"+s.namespace, getOpt)
			if err != nil {
				log.WithError(err).Fatal("Unable to contact etcd")
			}
			for _, node := range res.Node.Nodes {
				lbi := strings.LastIndexByte(node.Key, '/') + 1
				serviceID := node.Key[lbi:]
				color.Green("export SERVICEID=%s", serviceID)
				fmt.Println(node.Value)
				fmt.Println()
			}
		}}
	rootCmd.AddCommand(newCmd)
	rootCmd.AddCommand(lsCmd)
	rootCmd.AddCommand(editconfigCmd)
	rootCmd.AddCommand(dumpconfigCmd)
	rootCmd.AddCommand(loadconfigCmd)
}
