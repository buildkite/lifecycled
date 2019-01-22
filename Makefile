VERSION=$(shell git describe --tags --candidates=1 --dirty 2>/dev/null || echo "dev")
FLAGS=-s -w -X main.Version=$(VERSION)
SRC=$(shell find . -type f -name '*.go' -not -path "./vendor/*")

export GO111MODULE=on

lifecycled: *.go
	go build -o lifecycled -ldflags="$(FLAGS)" -v ./cmd/lifecycled

.PHONY: test
test:
	gofmt -s -l -w $(SRC)
	go vet -v ./...
	go test -race -v ./...

.PHONY: generate
generate:
	go generate ./...

.PHONY: clean
clean:
	rm -f lifecycled

.PHONY: release
release: arm64
# 	go get github.com/mitchellh/gox
	gox -ldflags="$(FLAGS)" -output="build/{{.Dir}}-{{.OS}}-{{.Arch}}" -osarch="freebsd/amd64 linux/386 linux/amd64 windows/amd64" ./cmd/lifecycled

# gox currenlty does not build arm64/aarch64 (https://github.com/mitchellh/gox/issues/92)
# Ensure we build both arm64 and aarch64 since `uname` can refer to the same arch using either name
.PHONE: arm64
arm64:
	GOOS=linux GOARCH=arm64 go build -o "build/lifecycled-linux-arm64" -ldflags="$(FLAGS)" -v ./cmd/lifecycled
	cp build/lifecycled-linux-arm64 build/lifecycled-linux-aarch64
