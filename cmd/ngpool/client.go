package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"github.com/davecgh/go-spew/spew"
	log "github.com/inconshreveable/log15"
	_ "github.com/spf13/viper/remote"
	"math/rand"
	"net"
)

type StratumClient struct {
	conn  net.Conn
	attrs map[string]string
	log   log.Logger
	id    string
}

func NewClient(conn net.Conn) *StratumClient {
	sc := &StratumClient{
		conn:  conn,
		id:    randomString(),
		attrs: map[string]string{},
	}
	sc.log = log.New("clientid", sc.id)
	return sc
}

func (c *StratumClient) Start() {
	go c.rwLoop()
}

func (c *StratumClient) rwLoop() {
	reader := bufio.NewReader(c.conn)
	writer := bufio.NewWriter(c.conn)
	for {
		raw, err := reader.ReadBytes('\n')
		if err != nil {
			c.log.Warn("Error reading", "err", err)
			return
		}
		var msg StratumMessage
		err = json.Unmarshal(raw, &msg)
		if err != nil {
			c.log.Warn("Error unmarshaling", "err", err)
			continue
		}
		spew.Dump(msg)
		c.log.Info("Recieved")
		switch msg.Method {
		case "mining.subscribe":
			if len(msg.Params) > 0 {
				userAgent, ok := msg.Params[0].(string)
				if ok {
					c.attrs["useragent"] = userAgent
					log.Debug("Client sent UserAgent", "agent", userAgent)
				}
			}
			diffSub := randomString()
			notifySub := randomString()
			respObj := StratumResponse{
				ID: msg.ID,
				Result: []interface{}{
					[]interface{}{"mining.set_difficulty", diffSub},
					[]interface{}{"mining.notify", notifySub},
				}}
			resp, err := json.Marshal(respObj)
			if err != nil {
				c.log.Error("Failed to serialize reply", "err", err)
				return
			}
			resp = append(resp, '\n')
			c.log.Debug("Sending response", "resp", respObj)
			writer.Write(resp)
			err = writer.Flush()
			if err != nil {
				c.log.Error("Failed to serialize reply", "err", err)
				return
			}
		default:
			c.log.Warn("Invalid message method", "method", msg.Method)
		}
	}
}

type StratumError struct {
	Code int
	Desc string
	TB   *string
}

type StratumResponse struct {
	ID     int64
	Result []interface{}
	Error  *StratumError
}

type StratumMessage struct {
	ID     int64
	Method string
	Params []interface{}
}

func randomString() string {
	randBytes := make([]byte, 4)
	rand.Read(randBytes)
	return hex.EncodeToString(randBytes)
}
