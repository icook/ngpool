#!/bin/bash -x
export CGO_CFLAGS_ALLOW=".*"
go-bindata -o cmd/ngweb/bindata.go ./sql/
go build ./cmd/ngsign
go build ./cmd/ngstratum
go build ./cmd/ngweb
go build ./cmd/ngcoinserver
go build ./cmd/ngctl
