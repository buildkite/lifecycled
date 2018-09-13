VERSION=$(shell git describe --tags --candidates=1 --dirty 2>/dev/null || echo "dev")
FLAGS=-s -w -X main.Version=$(VERSION)
SRC=$(shell find . -type f -name '*.go' -not -path "./vendor/*")

lifecycled: *.go
	go build -o lifecycled -ldflags="$(FLAGS)" -v cmd/main.go

generate:
	go generate ./...

test:
	gofmt -s -l -w $(SRC)
	go vet -v ./...
	go test -race -v ./...

.PHONY: clean
clean:
	rm -f lifecycled

.PHONY: release
release:
	go get github.com/mitchellh/gox
	gox -ldflags="$(FLAGS)" -output="build/{{.Dir}}-{{.OS}}-{{.Arch}}" -osarch="linux/amd64 windows/amd64" ./cmd
