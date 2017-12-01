package main

import (
	"fmt"
	"github.com/icook/btcd/rpcclient"
	"github.com/mitchellh/go-homedir"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

type Coinserver struct {
	Config  map[string]string
	client  *rpcclient.Client
	command *exec.Cmd
}

func NewCoinserver(overrideConfig map[string]string, blocknotify string) *Coinserver {
	// Set some defaults
	config := map[string]string{
		"blocknotify": blocknotify,
	}
	for key, val := range overrideConfig {
		config[key] = val
	}
	dir, _ := homedir.Expand(config["datadir"])
	config["datadir"] = dir
	config["pid"] = path.Join(config["datadir"], "coinserver.pid")
	args := []string{}
	for key, val := range config {
		args = append(args, "-"+key+"="+val)
	}
	log.Debug("Starting coinserver with config ", args)
	c := &Coinserver{
		Config:  config,
		command: exec.Command("litecoind", args...),
	}

	connCfg := &rpcclient.ConnConfig{
		Host:         fmt.Sprintf("localhost:%v", config["rpcport"]),
		User:         config["rpcuser"],
		Pass:         config["rpcpassword"],
		HTTPPostMode: true, // Bitcoin core only supports HTTP POST mode
		DisableTLS:   true, // Bitcoin core does not provide TLS by default
	}
	client, err := rpcclient.New(connCfg, nil)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize rpcclient")
	}
	c.client = client

	// Try to stop a coinserver from prvious run
	err = c.kill()
	if err == nil {
		log.Info("Killed coinserver that was still running")
	} else {
		log.Debug("Failed to kill previous run of coinserver: ", err)
	}

	return c
}

func (c *Coinserver) Stop() {
	err := c.kill()
	if err != nil {
		log.Warn("Unable to stop coinserver: ", err)
		return
	}
}

func (c *Coinserver) kill() error {
	pidStr, err := ioutil.ReadFile(c.Config["pid"])
	if err != nil {
		return errors.Wrap(err, "Can't load coinserver pidfile")
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(string(pidStr)), 10, 32)
	if err != nil {
		return errors.Wrap(err, "Can't parse coinserver pidfile")
	}
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return errors.Wrap(err, "No process on stop, exiting")
	}
	err = proc.Kill()
	if err != nil {
		return errors.Wrap(err, "Error exiting process")
	}
	// There's an annoying race condition here if we check the err value. If
	// the process exits before wait call runs then we get an error that is
	// annoying to test for, so we just don't worry about it
	proc.Wait()
	log.Info("Killed (hopefully) pid ", pid)
	return nil
}

func (c *Coinserver) Run() error {
	return c.command.Run()
}

func (c *Coinserver) WaitUntilUp() error {
	var err error
	i := 0
	tot := 900
	for ; i < tot; i++ {
		ret, err := c.client.GetInfo()
		if err == nil {
			log.Infof("Coinserver up after %v", i/10)
			log.Infof("\t-> getinfo=%+v", ret)
			break
		}
		time.Sleep(100 * time.Millisecond)
		if i%50 == 0 && i > 0 {
			log.Infof("Coinserver not up yet after %d / %d seconds", i/10, tot/10)
		}
	}
	return err
}
