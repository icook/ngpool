package main

import (
	"encoding/hex"
	"github.com/pkg/errors"
)

type StratumError struct {
	Code int
	Desc string
	TB   *string
}

const (
	StratumErrorOther     = 20
	StratumErrorStale     = 21
	StratumErrorDuplicate = 22
	StratumErrorLowDiff   = 23
	StratumErrorUnauth    = 24
	StratumErrorNotSubbed = 25
)

var stratumErrors = map[int]*StratumError{
	20: &StratumError{Code: 20, Desc: "Other/unknown", TB: nil},
	21: &StratumError{Code: 21, Desc: "Job not found (=stale)", TB: nil},
	22: &StratumError{Code: 22, Desc: "Duplicate share", TB: nil},
	23: &StratumError{Code: 23, Desc: "Low difficulty share", TB: nil},
	24: &StratumError{Code: 24, Desc: "Unauthorized worker", TB: nil},
	25: &StratumError{Code: 25, Desc: "Not subscribed", TB: nil},
}

type StratumResponse struct {
	ID     *int64      `json:"id"`
	Result interface{} `json:"result"`
	Error  interface{} `json:"error"`
}

type StratumMessage struct {
	ID     *int64      `json:"id"`
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

// Decoded params portions
type MiningSubscribe struct {
	UserAgent string
}

func DecodeMiningSubscribe(raw interface{}) *MiningSubscribe {
	params, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	ms := MiningSubscribe{}
	if len(params) > 0 {
		if userAgent, ok := params[0].(string); ok {
			ms.UserAgent = userAgent
		}
	}
	return &ms
}

type MiningAuthorize struct {
	Username string
	Password string
}

func DecodeMiningAuthorize(raw interface{}) (*MiningAuthorize, error) {
	params, ok := raw.([]interface{})
	if !ok {
		return nil, errors.New("Non array passed")
	}
	ma := MiningAuthorize{}
	if len(params) != 2 {
		return nil, errors.New("Authorize must provider two string fields")
	}
	if username, ok := params[0].(string); ok {
		ma.Username = username
	}
	if password, ok := params[1].(string); ok {
		ma.Password = password
	}
	return &ma, nil
}

type MiningSubmit struct {
	Username    string
	JobID       string `json:"job_id"`
	Extranonce2 []byte `json:"job_id"`
	Time        []byte `json:"ntime"`
	Nonce       []byte

	// Hacky, but we put the StratumMessage ID on here for easy replying from
	// different goroutine. Now our channel reciever doesn't have to make type
	// assertions...
	ID *int64
}

func (m *MiningSubmit) GetKey() string {
	// Generates a unique string for identifying duplicate shares
	return m.JobID + string(m.Extranonce2) + string(m.Time) + string(m.Nonce)
}

func DecodeMiningSubmit(raw interface{}) (*MiningSubmit, error) {
	params, ok := raw.([]interface{})
	if !ok {
		return nil, errors.New("Non array passed")
	}
	ma := MiningSubmit{}
	if len(params) != 5 {
		return nil, errors.New("Submit must have 5 fields")
	}
	if username, ok := params[0].(string); ok {
		ma.Username = username
	}
	if jobID, ok := params[1].(string); ok {
		ma.JobID = jobID
	}
	if extranonce2, ok := params[2].(string); ok {
		out, err := hex.DecodeString(extranonce2)
		if err != nil {
			return nil, err
		}
		ma.Extranonce2 = out
	}
	if time, ok := params[3].(string); ok {
		out, err := hex.DecodeString(time)
		if err != nil {
			return nil, err
		}
		ma.Time = out
	}
	if nonce, ok := params[4].(string); ok {
		out, err := hex.DecodeString(nonce)
		if err != nil {
			return nil, err
		}
		ma.Nonce = out
	}
	return &ma, nil
}
