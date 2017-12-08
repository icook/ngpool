package main

import (
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
	Method string      `json:"method,omitempty"`
	Error  interface{} `json:"error"`
}

type StratumMessage struct {
	ID     int64
	Method string
	Params []interface{}
}

// Decoded params portions
type MiningSubscribe struct {
	UserAgent string
}

func DecodeMiningSubscribe(params []interface{}) *MiningSubscribe {
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

func DecodeMiningAuthorize(params []interface{}) (*MiningAuthorize, error) {
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
