package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	log "github.com/inconshreveable/log15"
	_ "github.com/spf13/viper/remote"
	"io"
	"math/big"
	"math/rand"
	"net"
	"time"
)

type StratumClient struct {
	id string

	// State information
	attrs      map[string]string
	username   string
	subscribed bool
	diff       float64

	write        chan []byte
	jobListener  chan interface{}
	jobSubscribe chan chan interface{}
	newShare     chan *Share
	submit       chan *MiningSubmit
	log          log.Logger
	conn         net.Conn
}

var diff1 = big.Float{}

func init() {
	_, _, err := diff1.Parse(
		"0000ffff00000000000000000000000000000000000000000000000000000000", 16)
	if err != nil {
		panic(err)
	}
}

func NewClient(conn net.Conn, jobSubscribe chan chan interface{}, newShare chan *Share) *StratumClient {
	sc := &StratumClient{
		subscribed:   false,
		conn:         conn,
		id:           randomString(),
		attrs:        map[string]string{},
		jobSubscribe: jobSubscribe,
		jobListener:  make(chan interface{}),
		submit:       make(chan *MiningSubmit),
		write:        make(chan []byte, 10),
		newShare:     newShare,
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

type ClientJob struct {
	job           *Job
	id            string
	difficulty    float64
	submissionMap map[string]bool
}

func (c *StratumClient) Extranonce1() []byte {
	// We encode it from hex, so it must be right...
	out, _ := hex.DecodeString(c.id)
	return out
}

func (c *StratumClient) writeLoop() {
	defer c.Stop()

	jobBook := map[string]*ClientJob{}
	writer := bufio.NewWriter(c.conn)
	var resp []byte
	var raw interface{}
	var submission *MiningSubmit
	for {
		select {
		// Anything that writes to the client pushes onto this channel
		case resp = <-c.write:
			writer.Write(resp)
			err := writer.Flush()
			if err != nil {
				c.log.Debug("Error writing", "err", err)
				return // Disconnect
			}
		// Job listener won't recieve jobs until mining.authorize in the read
		// loop
		case submission = <-c.submit:
			if submission == nil {
				c.log.Error("Got nil on submit channel")
				return
			}
			clientJob, ok := jobBook[submission.JobID]
			if !ok {
				c.sendError(submission.ID, StratumErrorStale)
				continue
			}
			submissionKey := submission.GetKey()
			if _, ok := clientJob.submissionMap[submissionKey]; ok {
				c.sendError(submission.ID, StratumErrorDuplicate)
				continue
			}
			job := clientJob.job

			// Generate combined extranonce and diff target
			extranonce := append(c.Extranonce1(), submission.Extranonce2...)

			// TODO: Use the diff1 from algo configuration
			targetFl := big.Float{}
			targetFl.SetFloat64(clientJob.difficulty)
			targetFl.Mul(&diff1, &targetFl)
			target, _ := targetFl.Int(&big.Int{})
			blocks, validShare, currencies, err := job.CheckSolves(
				submission.Nonce, extranonce, target)
			if err != nil {
				c.log.Warn("Unexpected error CheckSolves", "job", clientJob)
				c.sendError(submission.ID, StratumErrorOther)
				continue
			}
			err = nil
			if validShare {
				err = c.send(&StratumResponse{
					ID:     submission.ID,
					Result: true,
				})
				if err != nil {
					c.log.Error("Failed write response", "err", err)
					return
				}
			} else {
				c.sendError(submission.ID, StratumErrorLowDiff)
				continue
			}
			clientJob.submissionMap[submissionKey] = true
			c.newShare <- &Share{
				username:   c.username,
				time:       time.Now(),
				currencies: currencies,
				difficulty: clientJob.difficulty,
				blocks:     blocks,
			}

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
			jobBook[jid] = &ClientJob{
				job:           newJob,
				id:            jid,
				difficulty:    c.diff,
				submissionMap: make(map[string]bool),
			}

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
			c.sendError(nil, StratumErrorOther)
			return
		}
		var msg StratumMessage
		err = json.Unmarshal(raw, &msg)
		if err != nil {
			c.log.Warn("Error unmarshaling", "err", err)
			c.sendError(nil, StratumErrorOther)
			continue
		}
		if msg.ID == nil {
			c.log.Warn("Null ID from StratumMessage")
			c.sendError(nil, StratumErrorOther)
			continue
		}
		c.log.Debug("Recieve", "msg", msg)
		switch msg.Method {
		case "mining.subscribe":
			if c.subscribed {
				c.sendError(msg.ID, StratumErrorOther)
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
				ID: msg.ID,
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
				c.sendError(msg.ID, StratumErrorNotSubbed)
				continue
			}
			ma, err := DecodeMiningAuthorize(msg.Params)
			if err != nil {
				c.sendError(msg.ID, StratumErrorOther)
				continue
			}
			// We ignore passwords
			c.username = ma.Username
			err = c.send(&StratumResponse{
				ID:     msg.ID,
				Result: true,
			})
			if err != nil {
				c.log.Error("Failed write response", "err", err)
				return
			}
			c.updateDiff()
			c.jobSubscribe <- c.jobListener
		case "mining.submit":
			if !c.subscribed {
				c.sendError(msg.ID, StratumErrorNotSubbed)
				continue
			}
			ms, err := DecodeMiningSubmit(msg.Params)
			if err != nil {
				c.sendError(msg.ID, StratumErrorOther)
				continue
			}
			ms.ID = msg.ID
			c.submit <- ms
		default:
			c.log.Warn("Invalid message method", "method", msg.Method)
		}
	}
}

func (c *StratumClient) sendError(id *int64, code int) error {
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
