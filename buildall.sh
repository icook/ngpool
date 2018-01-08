#!/bin/bash -x
go-bindata -o cmd/ngweb/bindata.go ./sql/
go build ./cmd/ngsign
go build ./cmd/ngstratum
go build ./cmd/ngweb
go build ./cmd/ngcoinserver
go build ./cmd/ngctl
