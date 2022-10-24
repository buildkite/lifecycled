SRC=$(shell find . -type f -name '*.go' -not -path "./vendor/*")

lifecycled: *.go
	goreleaser build --rm-dist --single-target

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
release:
	goreleaser release --skip-publish --rm-dist
