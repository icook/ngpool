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

	log    log.Logger
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

func NewClient(conn net.Conn) *StratumClient {
	sc := &StratumClient{
		conn:   conn,
		id:     randomString(),
		attrs:  map[string]string{},
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}
	sc.log = log.New("clientid", sc.id)
	return sc
}

func (c *StratumClient) Start() {
	go c.rwLoop()
}

func (c *StratumClient) rwLoop() {
	for {
		raw, err := c.reader.ReadBytes('\n')
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
				ID:     msg.ID,
				Result: true,
			})
			if err != nil {
				c.log.Error("Failed write response", "err", err)
				return
			}
		default:
			c.log.Warn("Invalid message method", "method", msg.Method)
		}
	}
}

func (c *StratumClient) sendError(id int64, code int) error {
	return c.send(&StratumResponse{
		ID:     id,
		Result: nil,
		Error:  stratumErrors[code],
	})
}

func (c *StratumClient) send(respObj *StratumResponse) error {
	resp, err := json.Marshal(respObj)
	if err != nil {
		return err
	}
	resp = append(resp, '\n')
	c.log.Debug("Sending response", "resp", respObj)
	c.writer.Write(resp)
	err = c.writer.Flush()
	if err != nil {
		return err
	}
	return nil
}

func randomString() string {
	randBytes := make([]byte, 4)
	rand.Read(randBytes)
	return hex.EncodeToString(randBytes)
}
