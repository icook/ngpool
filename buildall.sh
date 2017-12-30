#!/bin/bash -x
go build ./cmd/ngsign
go build ./cmd/ngstratum
go build ./cmd/ngweb
go build ./cmd/ngcoinserver
go build ./cmd/ngctl
