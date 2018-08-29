FROM golang:1.10 AS build-env

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

WORKDIR /go/src/github.com/buildkite/lifecycled/
ADD . /go/src/github.com/buildkite/lifecycled/
RUN go build -a -tags netgo -ldflags '-w' -o /bin/lifecycled/

FROM scratch
COPY --from=build-env /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build-env /bin/lifecycled lifecycled/
ENTRYPOINT ["/lifecycled"]
