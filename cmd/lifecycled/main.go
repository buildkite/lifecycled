package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/buildkite/lifecycled"

	cloudwatchlogs "github.com/kdar/logrus-cloudwatchlogs"
	"github.com/sirupsen/logrus"
)

var (
	Version string
)

func main() {
	app := kingpin.New("lifecycled",
		"Handle AWS autoscaling lifecycle events gracefully")

	app.Version(Version)
	app.DefaultEnvars()

	var (
		instanceID                   string
		snsTopic                     string
		disableSpotListener          bool
		handler                      *os.File
		handlerArgs                  []string
		jsonLogging                  bool
		debugLogging                 bool
		cloudwatchGroup              string
		cloudwatchStream             string
		spotListenerInterval         time.Duration
		autoscalingHeartbeatInterval time.Duration
	)

	app.Flag("instance-id", "The instance id to listen for events for").
		StringVar(&instanceID)

	app.Flag("sns-topic", "The SNS topic that receives events").
		StringVar(&snsTopic)

	app.Flag("no-spot", "Disable the spot termination listener").
		BoolVar(&disableSpotListener)

	app.Flag("handler", "The script to invoke to handle events").
		Required().
		FileVar(&handler)

	app.Flag("handler-args", "Additional arguments to pass to the script invoked by the handler").
		StringsVar(&handlerArgs)

	app.Flag("json", "Enable JSON logging").
		BoolVar(&jsonLogging)

	app.Flag("cloudwatch-group", "Write logs to a specific Cloudwatch Logs group").
		StringVar(&cloudwatchGroup)

	app.Flag("cloudwatch-stream", "Write logs to a specific Cloudwatch Logs stream, defaults to instance-id").
		StringVar(&cloudwatchStream)

	app.Flag("debug", "Show debugging info").
		BoolVar(&debugLogging)

	app.Flag("spot-listener-interval", "Interval to check for spot instance termination notices").
		Default("5s").
		DurationVar(&spotListenerInterval)

	app.Flag("autoscaling-heartbeat-interval", "Interval to send AWS Lifecycle Heartbeat Actions").
		Default("10s").
		DurationVar(&autoscalingHeartbeatInterval)

	app.Action(func(c *kingpin.ParseContext) error {
		logger := logrus.New()
		if jsonLogging {
			logger.SetFormatter(&logrus.JSONFormatter{})
		} else {
			logger.SetFormatter(&logrus.TextFormatter{})
		}

		if debugLogging {
			logger.SetLevel(logrus.DebugLevel)
		}

		region := os.Getenv("AWS_REGION")
		if region == "" {
			logger.Info("Looking up region from metadata service")
			sess, err := session.NewSession()
			if err != nil {
				logger.WithError(err).Fatal("Failed to create new aws session")
			}
			region, err = ec2metadata.New(sess).Region()
			if err != nil {
				logger.WithError(err).Fatal("Failed to look up region")
			}
		}

		sess, err := session.NewSession(&aws.Config{
			Region: aws.String(region),
		})
		if err != nil {
			logger.WithError(err).Fatal("Failed to create new aws session")
		}

		if instanceID == "" {
			logger.Info("Looking up instance id from metadata service")
			instanceID, err = ec2metadata.New(sess).GetMetadata("instance-id")
			if err != nil {
				logger.WithError(err).Fatal("Failed to lookup instance id")
			}
		}

		if cloudwatchStream == "" {
			cloudwatchStream = instanceID
		}

		if cloudwatchGroup != "" {
			hook, err := cloudwatchlogs.NewHook(cloudwatchGroup, cloudwatchStream, sess)
			if err != nil {
				logger.Fatal(err)
			}

			logger.WithFields(logrus.Fields{
				"group":  cloudwatchGroup,
				"stream": cloudwatchStream,
			}).Info("Writing logs to CloudWatch")

			logger.AddHook(hook)
			if !jsonLogging {
				logger.SetFormatter(&logrus.TextFormatter{
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
			for sig := range sigs {
				logger.WithField("signal", sig.String()).Info("Received signal: shutting down...")
				cancel()
				break
			}
		}()

		handler := lifecycled.NewFileHandler(handler, handlerArgs)
		daemon := lifecycled.New(&lifecycled.Config{
			InstanceID:                   instanceID,
			SNSTopic:                     snsTopic,
			SpotListener:                 !disableSpotListener,
			SpotListenerInterval:         spotListenerInterval,
			AutoscalingHeartbeatInterval: autoscalingHeartbeatInterval,
		}, sess, logger)

		notice, err := daemon.Start(ctx)
		if err != nil {
			return err
		}
		if notice != nil {
			log := logger.WithFields(logrus.Fields{"instanceId": instanceID, "notice": notice.Type()})
			log.Info("Executing handler")

			start, err := time.Now(), notice.Handle(ctx, handler, log)
			log = log.WithField("duration", time.Since(start).String())
			if err != nil {
				log.WithError(err).Error("Failed to execute handler")
			}
			log.Info("Handler finished successfully")

		}
		return nil
	})

	kingpin.MustParse(app.Parse(os.Args[1:]))
}
