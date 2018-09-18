package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kingpin"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/sns"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/buildkite/lifecycled"

	logrus_cloudwatchlogs "github.com/kdar/logrus-cloudwatchlogs"
	log "github.com/sirupsen/logrus"
)

var (
	Version string
)

func main() {
	app := kingpin.New("lifecycled",
		"Handle AWS autoscaling lifecycle events gracefully")

	app.Version(Version)
	app.Writer(os.Stdout)
	app.DefaultEnvars()

	var (
		instanceID       string
		snsTopic         string
		spotListener     bool
		handler          *os.File
		jsonLogging      bool
		debugLogging     bool
		cloudwatchGroup  string
		cloudwatchStream string
	)

	app.Flag("instance-id", "The instance id to listen for events for").
		StringVar(&instanceID)

	app.Flag("sns-topic", "The SNS topic that receives events").
		StringVar(&snsTopic)

	app.Flag("spot", "Listen for spot termination notices").
		BoolVar(&spotListener)

	app.Flag("handler", "The script to invoke to handle events").
		FileVar(&handler)

	app.Flag("json", "Enable JSON logging").
		BoolVar(&jsonLogging)

	app.Flag("cloudwatch-group", "Write logs to a specific Cloudwatch Logs group").
		StringVar(&cloudwatchGroup)

	app.Flag("cloudwatch-stream", "Write logs to a specific Cloudwatch Logs stream, defaults to instance-id").
		StringVar(&cloudwatchStream)

	app.Flag("debug", "Show debugging info").
		BoolVar(&debugLogging)

	app.Action(func(c *kingpin.ParseContext) error {
		if jsonLogging {
			log.SetFormatter(&log.JSONFormatter{})
		} else {
			log.SetFormatter(&log.TextFormatter{})
		}

		if debugLogging {
			log.SetLevel(log.DebugLevel)
		}

		sess, err := session.NewSession()
		if err != nil {
			log.WithError(err).Fatal("Failed to create new aws session")
		}

		if instanceID == "" {
			log.Info("Looking up instance id from metadata service")
			instanceID, err = ec2metadata.New(sess).GetMetadata("instance-id")
			if err != nil {
				log.WithError(err).Fatal("Failed to lookup instance id")
			}
		}

		if cloudwatchStream == "" {
			cloudwatchStream = instanceID
		}

		if cloudwatchGroup != "" {
			hook, err := logrus_cloudwatchlogs.NewHook(cloudwatchGroup, cloudwatchStream, aws.NewConfig())
			if err != nil {
				log.Fatal(err)
			}

			log.WithFields(log.Fields{
				"group":  cloudwatchGroup,
				"stream": cloudwatchStream,
			}).Info("Writing logs to CloudWatch")

			log.AddHook(hook)
			if !jsonLogging {
				log.SetFormatter(&log.TextFormatter{
					DisableColors:    true,
					DisableTimestamp: true,
				})
			}
		}

		sigs := make(chan os.Signal)
		defer close(sigs)

		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(sigs)

		// Create an execution context for the daemon that can be cancelled on OS signal
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			for signal := range sigs {
				log.WithField("signal", signal.String()).Info("Received signal: shutting down...")
				cancel()
				break
			}
		}()

		daemon := lifecycled.NewDaemon(instanceID, lifecycled.NewFileHandler(handler))

		if spotListener {
			daemon.AddListener(lifecycled.NewSpotListener(
				instanceID,
				ec2metadata.New(sess),
			))
		}

		if snsTopic != "" {
			daemon.AddListener(lifecycled.NewAutoscalingListener(
				instanceID,
				lifecycled.NewQueue(
					generateQueueName(instanceID),
					snsTopic,
					sqs.New(sess),
					sns.New(sess),
				),
				autoscaling.New(sess),
			))
		}

		return daemon.Start(ctx)
	})

	kingpin.MustParse(app.Parse(os.Args[1:]))
}

func generateQueueName(instanceID string) string {
	return fmt.Sprintf("lifecycled-%s", instanceID)
}