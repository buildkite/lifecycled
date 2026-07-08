package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/buildkite/lifecycled"

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
		jsonLogging                  bool
		debugLogging                 bool
		cloudwatchGroup              string
		cloudwatchStream             string
		tags                         string
		spotListenerInterval         time.Duration
		autoscalingHeartbeatInterval time.Duration
	)

	app.Flag("instance-id", "The instance id to listen for events for").
		StringVar(&instanceID)

	app.Flag("tags", "Comma separated list of tags to add to SQS queues").
		StringVar(&tags)

	app.Flag("sns-topic", "The SNS topic that receives events").
		StringVar(&snsTopic)

	app.Flag("no-spot", "Disable the spot termination listener").
		BoolVar(&disableSpotListener)

	app.Flag("handler", "The script to invoke to handle events").
		Required().
		FileVar(&handler)

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

	app.Flag("autoscaling-heartbeat-interval", "Interval to send AWS Lifecycle Heartbeat Actions; keep shorter than the hook's HeartbeatTimeout").
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

		// Cancelled on SIGINT/SIGTERM by the signal handler below.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// LoadDefaultConfig resolves region and credentials from the environment
		// and shared config. WithEC2IMDSRegion is deliberately not used here: it
		// makes an unreachable IMDS (i.e. running off EC2) a fatal config-load
		// error, masking the clearer "no region" message below. Fall back to IMDS
		// explicitly and ignore its error so an off-EC2 run reports the real cause.
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			logger.WithError(err).Fatal("Failed to load AWS configuration")
		}
		if cfg.Region == "" {
			if out, err := imds.NewFromConfig(cfg).GetRegion(ctx, &imds.GetRegionInput{}); err == nil {
				cfg.Region = out.Region
			}
		}
		if cfg.Region == "" {
			logger.Fatal("No region resolved; set AWS_REGION, AWS_DEFAULT_REGION, or a profile region")
		}
		logger.WithField("region", cfg.Region).Info("Using region")

		if instanceID == "" {
			logger.Info("Looking up instance id from metadata service")
			out, err := imds.NewFromConfig(cfg).GetMetadata(ctx, &imds.GetMetadataInput{Path: "instance-id"})
			if err != nil {
				logger.WithError(err).Fatal("Failed to lookup instance id")
			}
			b, err := io.ReadAll(out.Content)
			_ = out.Content.Close()
			if err != nil {
				logger.WithError(err).Fatal("Failed to read instance id")
			}
			instanceID = strings.TrimSpace(string(b))
		}

		if cloudwatchStream == "" {
			cloudwatchStream = instanceID
		}

		if cloudwatchGroup != "" {
			hook, err := lifecycled.NewCloudWatchLogsHook(ctx, cloudwatchlogs.NewFromConfig(cfg), cloudwatchGroup, cloudwatchStream)
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

		sigs := make(chan os.Signal, 1)
		defer close(sigs)

		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(sigs)

		go func() {
			for sig := range sigs {
				// Cancel before logging: with the CloudWatch hook enabled this line
				// ships synchronously, so a slow endpoint must not delay cancelling
				// the drain.
				cancel()
				logger.WithField("signal", sig.String()).Info("Received signal: shutting down...")
				break
			}
		}()

		handler := lifecycled.NewFileHandler(handler)
		daemonConfig := &lifecycled.Config{
			InstanceID:                   instanceID,
			Tags:                         tags,
			SNSTopic:                     snsTopic,
			SpotListener:                 !disableSpotListener,
			SpotListenerInterval:         spotListenerInterval,
			AutoscalingHeartbeatInterval: autoscalingHeartbeatInterval,
		}
		if err := daemonConfig.Validate(); err != nil {
			logger.WithError(err).Fatal("Invalid configuration")
		}
		daemon := lifecycled.New(daemonConfig, cfg, logger)

		notice, err := daemon.Start(ctx)
		if err != nil {
			return err
		}
		if notice != nil {
			log := logger.WithFields(logrus.Fields{"instanceId": instanceID, "notice": notice.Type()})
			log.Info("Executing handler")

			// The handler runs on the signal-cancellable ctx, so a SIGINT/SIGTERM
			// mid-handle intentionally cancels the drain script; the autoscaling notice
			// still releases the ASG hook via CompleteLifecycleAction on a fresh context.
			start := time.Now()
			err = notice.Handle(ctx, handler, log)
			log = log.WithField("duration", time.Since(start).String())
			if err != nil {
				log.WithError(err).Error("Failed to execute handler")
				return err
			}
			log.Info("Handler finished successfully")
		}
		return nil
	})

	kingpin.MustParse(app.Parse(os.Args[1:]))
}
