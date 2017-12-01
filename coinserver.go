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

	c := &Coinserver{
		Config: config,
	}

	// TODO: This will put warnings into log on startup...
	// Stop a previous run. Might happen from segfaults, etc
	log.Info("Trying to stop coinserver from previous run")
	if c.Stop() {
		// Wait for cleanup from closing coinserver
		time.Sleep(time.Second * 5)
	}

	// Start server
	log.Debug("Starting coinserver with config ", args)
	c.command = exec.Command("litecoind", args...)

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

	return c
}

func (c *Coinserver) Stop() bool {
	proc, err := c.getProcess()
	if err != nil {
		log.WithError(err).Warn("Failed to lookup pid for process from pidfile")
		return false
	}
	err = c.signalExit(proc, time.Second*30)
	if err != nil {
		log.WithError(err).Warn("Failed graceful shutdown")
	} else {
		return true
	}
	err = c.kill(proc)
	if err != nil {
		log.WithError(err).Warn("Unable to stop coinserver")
		return false
	}
	return true
}

func (c *Coinserver) getProcess() (*os.Process, error) {
	pidStr, err := ioutil.ReadFile(c.Config["pid"])
	if err != nil {
		return nil, errors.Wrap(err, "Can't load coinserver pidfile")
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(string(pidStr)), 10, 32)
	if err != nil {
		return nil, errors.Wrap(err, "Can't parse coinserver pidfile")
	}
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return nil, errors.Wrap(err, "Failed to find process from pidfile")
	}
	return proc, nil
}

func (c *Coinserver) signalExit(proc *os.Process, timeout time.Duration) error {
	startShutdown := time.Now()
	err := proc.Signal(os.Interrupt)
	if err != nil {
		return errors.Wrap(err, "Failed to signal exit to coinserver")
	}
	log.WithField("pid", proc.Pid).Info("SIGINT to coinserver")
	done := make(chan interface{}, 1)
	go func() {
		proc.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.WithField("time", time.Now().Sub(startShutdown)).Info("Coinserver shutdown complete")
	case <-time.After(timeout):
		return errors.Errorf("SIGINT timeout %v reached", timeout)
	}
	return nil
}

func (c *Coinserver) kill(proc *os.Process) error {
	err := proc.Kill()
	if err != nil {
		return errors.Wrap(err, "Error exiting process")
	}
	// There's an annoying race condition here if we check the err value. If
	// the process exits before wait call runs then we get an error that is
	// annoying to test for, so we just don't worry about it
	proc.Wait()
	log.Info("Killed (hopefully) pid ", proc.Pid)
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
