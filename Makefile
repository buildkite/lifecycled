VERSION=$(shell git describe --tags --candidates=1 --dirty 2>/dev/null || echo "dev")
FLAGS=-s -w -X main.Version=$(VERSION)

lifecycled: *.go
	go install -a -ldflags="$(FLAGS)"
	go build -v -ldflags="$(FLAGS)"

.PHONY: clean
clean:
	rm -f lifecycled

.PHONY: release
release:
	go get github.com/mitchellh/gox
	gox -ldflags="$(FLAGS)" -output="build/{{.Dir}}-{{.OS}}-{{.Arch}}" -osarch="linux/amd64 windows/amd64"
