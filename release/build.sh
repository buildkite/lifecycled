#!/bin/sh -ex
bin="builds/lifecycled-linux-$(uname -m)"
mkdir -p builds/
CGO_ENABLED=0 go build -ldflags "-s" -a -installsuffix cgo -o $bin ./cli/lifecycled
