package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/coreos/etcd/client"
	log "github.com/inconshreveable/log15"
	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/spf13/cobra"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var RootCmd = &cobra.Command{
	Use:   "ngctl",
	Short: "A utility for ngpool",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var endpoints []string

func init() {
	RootCmd.PersistentFlags().StringSliceVar(
		&endpoints, "endpoints", []string{"http://127.0.0.1:4001", "http://127.0.0.1:2379"}, "gRPC endpoints")
}

func getDefaultConfig(serviceType string) string {
	return ""
}

func getEtcdKeys() client.KeysAPI {
	cfg := client.Config{
		Endpoints: endpoints,
		Transport: client.DefaultTransport,
		// set timeout per request to fail fast when the target endpoint is unavailable
		HeaderTimeoutPerRequest: time.Second,
	}
	etcd, err := client.New(cfg)
	if err != nil {
		log.Crit("Failed to make etcd client", "err", err)
		os.Exit(1)
	}
	keysAPI := client.NewKeysAPI(etcd)
	return keysAPI
}

func modifyLoop(currentVal string, keyPath string) (string, bool) {
	tmpFile := mktmp(currentVal)
	defer os.Remove(tmpFile.Name())

	for {
		newConfig := editFile(tmpFile.Name())
		dmp := diffmatchpatch.New()
		diffs := dmp.DiffMain(currentVal, newConfig, false)
		// Exit if no change
		if newConfig == currentVal {
			return "", false
		}
		fmt.Println(dmp.DiffPrettyText(diffs))
		for {
			reader := bufio.NewReader(os.Stdin)
			fmt.Print("Push changes (y,n,e): ")
			text, _ := reader.ReadString('\n')
			input := strings.TrimSpace(text)
			if input == "y" {
				return newConfig, true
			} else if input == "e" {
				break
			} else if input != "n" {
				continue
			}
			return "", false
		}
	}
}

func editFile(fpath string) string {
	// Launch editor with our tmp file
	editor := "vi"
	editorPath, err := exec.LookPath(editor)
	if err != nil {
		log.Crit("Failed editor path lookup", "err", err, "editor", editor)
		os.Exit(1)
	}
	cmd := exec.Command(editorPath, fpath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Start()
	if err != nil {
		log.Crit("Failed to start editor", "err", err)
		os.Exit(1)
	}
	err = cmd.Wait()

	// Read in edited config
	newConfigByte, err := ioutil.ReadFile(fpath)
	newConfig := string(newConfigByte)
	if err != nil {
		log.Crit("Somehow we fail to read a file we just made...", "err", err)
		os.Exit(1)
	}
	return newConfig
}

func mktmp(contents string) *os.File {
	// Generate a new temporary file with our config from the server
	randSuffix := time.Now().UnixNano() + int64(os.Getpid())
	fname := fmt.Sprintf("%d_cfgscratch.yaml", randSuffix)
	fpath := filepath.Join(os.TempDir(), fname)
	tmpFile, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		log.Crit("Failed to make file, maybe another editor is open now?",
			"fname", fname, "err", err)
		os.Exit(1)
	}
	_, err = tmpFile.WriteString(contents)
	if err != nil {
		log.Crit("Failed to write tmp file", "err", err)
		os.Exit(1)
	}
	tmpFile.Close()
	return tmpFile
}

func rmKey(etcdKeys client.KeysAPI, configKeyPath string) {
	// Get current config
	_, err := etcdKeys.Delete(context.Background(), configKeyPath, nil)
	if err != nil {
		log.Crit("Failed to rm key", "key", configKeyPath, "err", err)
		os.Exit(1)
	}
	log.Info("Removed config", "keypath", configKeyPath)
}

func getKey(etcdKeys client.KeysAPI, configKeyPath string) string {
	// Get current config
	configResp, err := etcdKeys.Get(context.Background(), configKeyPath, nil)
	var currentVal = ""
	if cerr, ok := err.(client.Error); ok && cerr.Code == client.ErrorCodeKeyNotFound {
		log.Warn("Key empty, starting empty", "key", configKeyPath)
	} else if err != nil {
		log.Crit("Failed fetching config", "err", err)
		os.Exit(1)
	} else {
		currentVal = string(configResp.Node.Value)
	}
	return currentVal
}

func editKey(etcdKeys client.KeysAPI, configKeyPath string) {
	currentVal := getKey(etcdKeys, configKeyPath)
	newConfig, save := modifyLoop(currentVal, configKeyPath)
	if !save {
		return
	}
	writeKey(etcdKeys, configKeyPath, newConfig)
}

func writeKey(etcdKeys client.KeysAPI, configKeyPath string, newConfig string) {
	_, err := etcdKeys.Set(context.Background(), configKeyPath, newConfig, nil)
	if err != nil {
		log.Crit("Failed pushing config, dumping", "err", err)
		fmt.Println(newConfig)
		os.Exit(1)
	}
	log.Info("Successfully wrote config", "keypath", configKeyPath)
}
