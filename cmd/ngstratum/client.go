package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/dustin/go-broadcast"
	log "github.com/inconshreveable/log15"
	"github.com/mitchellh/mapstructure"

	"github.com/icook/ngpool/pkg/common"
)

type StratumClient struct {
	id string

	// State information
	attrs       map[string]string
	username    string
	worker      string
	subscribed  bool
	rpcVersion2 bool
	// This is a horrible hack. We need to push a job in the response to the
	// login command, and to comply we need to respond with the appropriate ID.
	// The ID of login command gets stored here temporarily since we have to
	// wait for a job to be pushed and handle the response as a special case
	loginMsgID int64
	diff       float64

	write       chan []byte
	jobListener chan interface{}
	jobCast     broadcast.Broadcaster
	newShare    chan *Share
	submit      chan *MiningSubmit
	vardiff     *VarDiff
	shutdown    chan interface{}
	hasShutdown bool
	shareWindow common.Window
	log         log.Logger
	conn        net.Conn
}

var diff1 = big.Float{}
var XMRdiff1 = big.Int{}

func init() {
	_, _, err := diff1.Parse(
		"0000ffff00000000000000000000000000000000000000000000000000000000", 16)
	if err != nil {
		panic(err)
	}

	XMRdiff1.SetString(
		"0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 0)
}

func NewClient(conn net.Conn, jobCast broadcast.Broadcaster, newShare chan *Share, vardiff *VarDiff) *StratumClient {
	sc := &StratumClient{
		rpcVersion2: false,
		subscribed:  false,
		conn:        conn,
		id:          randomString(),
		attrs:       map[string]string{},
		jobCast:     jobCast,
		jobListener: make(chan interface{}),
		shutdown:    make(chan interface{}),
		submit:      make(chan *MiningSubmit),
		vardiff:     vardiff,
		write:       make(chan []byte, 10),
		newShare:    newShare,
		shareWindow: common.NewWindow(50),
	}
	sc.log = log.New("clientid", sc.id)
	return sc
}

func (c *StratumClient) Stop() {
	// Either write or read thread exit trigger shutdown, so it might get
	// called multiple times
	if c.hasShutdown {
		return
	}

	c.log.Info("Client disconnect")
	close(c.shutdown)
	c.hasShutdown = true
	err := c.conn.Close()
	c.jobCast.Unregister(c.jobListener)
	if err != nil {
		c.log.Warn("Error closing", "err", err)
	}
}

func (c *StratumClient) Start() {
	go c.readLoop()
	go c.writeLoop()
}

// Handle calculating a users difficulty and push a write if it's changed
func (c *StratumClient) updateDiff() error {
	rate := c.shareWindow.RateMinute()
	newDiff := c.vardiff.ComputeNew(c.diff, rate)
	if c.diff == newDiff {
		return nil
	}
	c.log.Info("Moving to new diff", "diff", newDiff, "rate", rate)
	c.diff = newDiff
	if !c.rpcVersion2 {
		return c.send(&StratumMessage{
			Method: "mining.set_difficulty",
			Params: []float64{c.diff},
		})
	}
	return nil
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

func (c *StratumClient) status() common.StratumClientStatus {
	return common.StratumClientStatus{
		c.username,
		c.shareWindow.RateSecond() * 65536,
		c.worker,
		c.diff,
	}
}

// Taken directly from https://github.com/sammy007/monero-stratum/util/util.go
// All original copyrights apply
func GetTargetHex(diff int64) string {
	padded := make([]byte, 32)
	diffBuff := new(big.Int).Div(&XMRdiff1, big.NewInt(diff)).Bytes()
	copy(padded[32-len(diffBuff):], diffBuff)
	buff := padded[0:4]
	common.ReverseBytes(buff)
	targetHex := hex.EncodeToString(buff)
	return targetHex
}

func (c *StratumClient) writeLoop() {
	defer c.Stop()

	jobBook := map[string]*ClientJob{}
	writer := bufio.NewWriter(c.conn)
	var resp []byte
	var raw interface{}
	var ticker = time.NewTicker(time.Second * 60)
	var submission *MiningSubmit
	for {
		select {
		case <-c.shutdown:
			return
		// Anything that writes to the client pushes onto this channel
		case resp = <-c.write:
			writer.Write(resp)
			err := writer.Flush()
			if err != nil {
				c.log.Debug("Error writing", "err", err)
				return // Disconnect
			}
		// Periodically recalculate difficulty
		case <-ticker.C:
			c.updateDiff()
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
				worker:     c.worker,
				time:       time.Now(),
				currencies: currencies,
				difficulty: clientJob.difficulty,
				blocks:     blocks,
			}
			c.shareWindow.Add(clientJob.difficulty)

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

			if c.rpcVersion2 {
				params, err := newJob.GetStratum2Params(c.Extranonce1())
				if err != nil {
					c.log.Error("Failed to get stratum params", "err", err)
					continue
				}
				params["target"] = GetTargetHex(int64(c.diff))
				params["job_id"] = jid

				if !c.subscribed {
					c.subscribed = true
					err = c.send(&Stratum2Response{
						ID:      &c.loginMsgID,
						JSONRPC: "2.0",
						Result: map[string]interface{}{
							"id":     c.id,
							"status": "OK",
							"job":    params,
						}})
					if err != nil {
						c.log.Error("Failed write response", "err", err)
						return
					}
				} else {
					err = c.send(&Stratum2Message{
						JSONRPC: "2.0",
						Method:  "job",
						Params:  params,
					})
					if err != nil {
						c.log.Error("Failed write response", "err", err)
						return
					}
				}
			} else {
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

}

func (c *StratumClient) authorize() {
	c.updateDiff()
	c.log.Debug("Subscribing to jobs")
	c.jobCast.Register(c.jobListener)
	// Start the time window for hashrate average right now
	c.shareWindow.Add(0)
}

func parseUser(input string) (string, string) {
	// We ignore passwords. Trim worker name just in case
	var username, worker string
	if len(input) > 65 {
		input = input[:65]
	}
	parts := strings.SplitN(input, ".", 2)
	if len(parts) == 2 {
		worker = parts[1]
	}
	username = parts[0]
	return username, worker
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
			if bytes.TrimSpace(raw) == nil {
				continue
			}
			c.log.Warn("Error unmarshaling", "err", err, "content", string(raw))
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
			c.username, c.worker = parseUser(ma.Username)
			err = c.send(&StratumResponse{
				ID:     msg.ID,
				Result: true,
			})
			if err != nil {
				c.log.Error("Failed write response", "err", err)
				return
			}
			c.authorize()
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
		// JSON RPC 2.0 -------------------------------------
		case "submit":
			var ms2 MiningSubmit2
			err := mapstructure.Decode(msg.Params, &ms2)
			if err != nil {
				c.sendError(msg.ID, StratumErrorOther)
				continue
			}

			nonce, _ := hex.DecodeString(ms2.Nonce)
			ms := &MiningSubmit{
				JobID:       ms2.JobID,
				Nonce:       nonce,
				Extranonce2: []byte{0, 0, 0, 0},
			}
			c.submit <- ms
		case "login":
			c.rpcVersion2 = true
			c.loginMsgID = *msg.ID
			var login Login
			err := mapstructure.Decode(msg.Params, &login)
			if err != nil {
				c.sendError(msg.ID, StratumErrorOther)
				continue
			}
			c.username, c.worker = parseUser(login.Login)
			c.attrs["useragent"] = login.Agent
			c.authorize()
		default:
			c.log.Warn("Invalid message method", "method", msg.Method)
		}

		if c.hasShutdown {
			return
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
