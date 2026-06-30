module github.com/buildkite/lifecycled

go 1.26.4

require (
	github.com/alecthomas/kingpin v0.0.0-20180312062423-a39589180ebd
	github.com/aws/aws-sdk-go-v2 v1.42.0
	github.com/aws/aws-sdk-go-v2/config v1.32.25
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.29
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.67.4
	github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs v1.78.0
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.308.0
	github.com/aws/aws-sdk-go-v2/service/sns v1.40.1
	github.com/aws/aws-sdk-go-v2/service/sqs v1.44.0
	github.com/aws/aws-sdk-go-v2/service/sts v1.43.3
	github.com/aws/smithy-go v1.27.2
	github.com/sirupsen/logrus v1.8.3
	go.uber.org/mock v0.6.0
)

require (
	github.com/alecthomas/template v0.0.0-20190718012654-fb15b899a751 // indirect
	github.com/alecthomas/units v0.0.0-20190924025748-f65c72e2690d // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.13 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.19.24 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.29 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.29 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.30 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.29 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.2.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.31.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.36.6 // indirect
	golang.org/x/mod v0.27.0 // indirect
	golang.org/x/sync v0.16.0 // indirect
	golang.org/x/sys v0.35.0 // indirect
	golang.org/x/tools v0.36.0 // indirect
)

tool go.uber.org/mock/mockgen
