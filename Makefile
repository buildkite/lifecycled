SRC=$(shell find . -type f -name '*.go' -not -path "./vendor/*")

lifecycled: *.go
	goreleaser build --rm-dist --single-target

.PHONY: test
test: generate
	gofmt -s -l -w $(SRC)
	go vet -v ./...
	go test -race -v ./...

.PHONY: generate
generate:
	go generate ./...

.PHONY: clean
clean:
	git clean -ffidx -e Session.vim

.PHONY: release
release:
	goreleaser release --skip-publish --rm-dist
