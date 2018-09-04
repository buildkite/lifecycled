# Lifecycled Tester

This contains a set of Cloudformation templates and scripts for creating an autoscaling group of instances with a specific version of lifecycled on it for testing.

## Setup

A base cloudformation stack is needed with the pieces that are slow to create and can be re-used across tests.

```bash
parfait create-stack -t ./tools/tester/cloudformation/base.yml lifecycled-test-base
```

# Running

```
GOOS=linux GOARCH=amd64 go build -a -tags netgo -ldflags '-w' -o lifecycled-linux-amd64 .
./tools/tester/test.sh
```
