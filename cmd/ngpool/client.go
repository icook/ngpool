package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	log "github.com/inconshreveable/log15"
	_ "github.com/spf13/viper/remote"
	"io"
	"math/rand"
	"net"
)

type StratumClient struct {
	attrs      map[string]string
	username   string
	id         string
	subscribed bool
	diff       float64

	write        chan []byte
	jobListener  chan interface{}
	jobSubscribe chan chan interface{}
	log          log.Logger
	conn         net.Conn
}

func NewClient(conn net.Conn, jobSubscribe chan chan interface{}) *StratumClient {
	sc := &StratumClient{
		subscribed:   false,
		conn:         conn,
		id:           randomString(),
		attrs:        map[string]string{},
		jobSubscribe: jobSubscribe,
		jobListener:  make(chan interface{}),
		write:        make(chan []byte, 10),
	}
	sc.log = log.New("clientid", sc.id)
	return sc
}

func (c *StratumClient) Stop() {
	c.log.Info("Stop")
	err := c.conn.Close()
	if err != nil {
		c.log.Warn("Error closing", "err", err)
	}
}

func (c *StratumClient) Start() {
	go c.readLoop()
	go c.writeLoop()
}

func (c *StratumClient) updateDiff() error {
	newDiff := 0.1
	if c.diff == newDiff {
		return nil
	}
	c.diff = newDiff
	// Handle calculating a users difficulty and push a write if it's changed
	return c.send(&StratumMessage{
		Method: "mining.set_difficulty",
		Params: []float64{c.diff},
	})
}

func (c *StratumClient) writeLoop() {
	defer c.Stop()

	jobBook := map[string]*Job{}
	writer := bufio.NewWriter(c.conn)
	var resp []byte
	var raw interface{}
	for {
		select {
		// Anything that writes to the client pushes onto this channel
		case resp = <-c.write:
			c.log.Debug("Writing response", "resp", string(resp))
			writer.Write(resp)
			err := writer.Flush()
			if err != nil {
				c.log.Debug("Error writing", "err", err)
				return // Disconnect
			}
		// Job listener won't recieve jobs until mining.authorize in the read
		// loop
		case raw = <-c.jobListener:
			if raw == nil {
				c.log.Debug("Closing job listener")
				return
			}
			newJob, ok := raw.(*Job)
			if !ok {
				c.log.Warn("Bad job from broadcast", "job", raw)
				continue
			}
			jid := randomString()
			jobBook[jid] = newJob

			params, err := newJob.GetStratumParams()
			if err != nil {
				c.log.Error("Failed to get stratum params", "err", err)
				continue
			}
			params = append([]interface{}{jid}, params...)

			err = c.send(&StratumMessage{
				Method: "mining.notify",
				Params: params,
			})
			if err != nil {
				c.log.Error("Failed write response", "err", err)
				return
			}
		}
	}

}
func (c *StratumClient) readLoop() {
	defer c.Stop()

	reader := bufio.NewReader(c.conn)
	for {
		raw, err := reader.ReadBytes('\n')
		if err == io.EOF {
			c.log.Debug("Closed connection")
			return
		}
		if err != nil {
			c.log.Warn("Error reading", "err", err)
			c.sendError(-1, StratumErrorOther)
			return
		}
		var msg StratumMessage
		err = json.Unmarshal(raw, &msg)
		if err != nil {
			c.log.Warn("Error unmarshaling", "err", err)
			c.sendError(-1, StratumErrorOther)
			continue
		}
		c.log.Debug("Recieve", "msg", msg)
		switch msg.Method {
		case "mining.subscribe":
			if c.subscribed {
				c.sendError(-1, StratumErrorOther)
				continue
			}
			ms := DecodeMiningSubscribe(msg.Params)
			if ms.UserAgent != "" {
				c.attrs["useragent"] = ms.UserAgent
				log.Debug("Client sent UserAgent", "agent", ms.UserAgent)
			}
			// We don't store these for now, no resume functionality is
			// provided. Effectively these are junk
			diffSub := randomString()
			notifySub := randomString()
			err = c.send(&StratumResponse{
				ID: *msg.ID,
				Result: []interface{}{
					[]interface{}{
						[]interface{}{"mining.set_difficulty", diffSub},
						[]interface{}{"mining.notify", notifySub},
					},
					c.id, // A per connection extranonce to ensure they're iterating different attempts from peers
					4,    // extranonce2 size (the one they iterate)
				}})
			if err != nil {
				c.log.Error("Failed write response", "err", err)
				return
			}
			c.subscribed = true
		case "mining.authorize":
			if !c.subscribed {
				c.sendError(-1, StratumErrorNotSubbed)
				continue
			}
			ma, err := DecodeMiningAuthorize(msg.Params)
			if err != nil {
				c.sendError(-1, StratumErrorOther)
				continue
			}
			// We ignore passwords
			c.username = ma.Username
			err = c.send(&StratumResponse{
				ID:     *msg.ID,
				Result: true,
			})
			if err != nil {
				c.log.Error("Failed write response", "err", err)
				return
			}
			c.updateDiff()
			c.jobSubscribe <- c.jobListener
		default:
			c.log.Warn("Invalid message method", "method", msg.Method)
		}
	}
}

func (c *StratumClient) sendError(id int64, code int) error {
	err := stratumErrors[code]
	resp := &StratumResponse{
		ID:     id,
		Result: nil,
		Error:  []interface{}{err.Code, err.Desc, err.TB},
	}
	return c.send(resp)
}

func (c *StratumClient) send(respObj interface{}) error {
	resp, err := json.Marshal(respObj)
	if err != nil {
		return err
	}
	resp = append(resp, '\n')
	c.log.Debug("Sending response", "resp", respObj)
	c.write <- resp
	return nil
}

func randomString() string {
	randBytes := make([]byte, 4)
	rand.Read(randBytes)
	return hex.EncodeToString(randBytes)
}
