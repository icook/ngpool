package main

import (
	"fmt"
	"github.com/icook/btcd/rpcclient"
	log "github.com/inconshreveable/log15"
	"github.com/mitchellh/go-homedir"
	"github.com/pkg/errors"
	"io"
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

func NewCoinserver(overrideConfig map[string]string, blocknotify string, coinserverBinary string) *Coinserver {
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
	c.command = exec.Command(coinserverBinary, args...)

	connCfg := &rpcclient.ConnConfig{
		Host:         fmt.Sprintf("localhost:%v", config["rpcport"]),
		User:         config["rpcuser"],
		Pass:         config["rpcpassword"],
		HTTPPostMode: true, // Bitcoin core only supports HTTP POST mode
		DisableTLS:   true, // Bitcoin core does not provide TLS by default
	}
	client, err := rpcclient.New(connCfg, nil)
	if err != nil {
		log.Crit("Failed to initialize rpcclient", "err", err)
	}
	c.client = client

	return c
}

func (c *Coinserver) Stop() bool {
	proc, err := c.getProcess()
	if err != nil {
		log.Warn("Failed to lookup pid for process from pidfile", "err", err)
		return false
	}
	err = c.signalExit(proc, time.Second*30)
	if err != nil {
		log.Warn("Failed graceful shutdown", "err", err)
	} else {
		return true
	}
	err = c.kill(proc)
	if err != nil {
		log.Warn("Unable to stop coinserver", "err", err)
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
	log.Info("SIGINT to coinserver", "pid", proc.Pid)
	done := make(chan interface{}, 1)
	go func() {
		proc.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.Info("Coinserver shutdown complete", "time", time.Now().Sub(startShutdown))
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
	log.Debug("Starting coinserver", "bin", c.command.Path, "args", c.command.Args)
	done := make(chan interface{}, 1)
	buf := make([]byte, 2048)
	stderr, err := c.command.StderrPipe()
	if err != nil {
		log.Error("Failed to read stderr of coinserver", "err", err)
	}
	go func() {
		io.ReadFull(stderr, buf)
		close(done)
	}()
	c.command.Start()
	select {
	case <-done:
		log.Info("Coinserver exited early", "err", string(buf))
		return errors.New("Coinserver exited early")
	case <-time.After(time.Second * 2):
	}
	return nil
}

func (c *Coinserver) WaitUntilUp() error {
	var err error
	i := 0
	tot := 900
	for ; i < tot; i++ {
		ret, err := c.client.GetInfo()
		if err == nil {
			log.Info("Coinserver up", "time", i/10, "info", ret)
			break
		}
		time.Sleep(100 * time.Millisecond)
		if i%50 == 0 && i > 0 {
			log.Info("Coinserver not up yet", "elap", i/10, "total", tot/10)
		}
	}
	return err
}
