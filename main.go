package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kingpin"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"

	logrus_cloudwatchlogs "github.com/kdar/logrus-cloudwatchlogs"
	log "github.com/sirupsen/logrus"
)

var (
	Version string
)

const (
	metadataURLInstanceID = "http://169.254.169.254/latest/meta-data/instance-id"
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

		if instanceID == "" {
			log.Infof("Looking up instance id from metadata service")
			id, err := getInstanceID()
			if err != nil {
				log.Fatalf("Failed to lookup instance id: %v", err)
			}
			instanceID = id
		}

		if cloudwatchStream == "" {
			cloudwatchStream = instanceID
		}

		if cloudwatchGroup != "" {
			hook, err := logrus_cloudwatchlogs.NewHook(cloudwatchGroup, cloudwatchStream, aws.NewConfig())
			if err != nil {
				log.Fatal(err)
			}

			log.Infof("Writing logs to Cloudwatch Group %s, Stream %s", cloudwatchGroup, cloudwatchStream)
			log.AddHook(hook)

			if !jsonLogging {
				log.SetFormatter(&log.TextFormatter{
					DisableColors:    true,
					DisableTimestamp: true,
				})
			}
		}

		sess, err := session.NewSession()
		if err != nil {
			log.WithError(err).Fatal("Failed to create new session")
		}

		sigs := make(chan os.Signal, 2)
		defer close(sigs)

		signal.Notify(sigs,
			syscall.SIGHUP,
			syscall.SIGINT,
			syscall.SIGTERM,
			syscall.SIGQUIT,
			syscall.SIGPIPE)
		defer signal.Stop(sigs)

		// Create an execution context for the daemon that can be cancelled on OS signal
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			for signal := range sigs {
				log.Infof("Received signal (%s) shutting down...", signal)
				cancel()
				break
			}
		}()

		daemon := Daemon{
			InstanceID: instanceID,
			Handler:    handler,

			SpotMonitor: &SpotMonitor{
				InstanceID: instanceID,
				Handler:    handler,
			},
		}

		if snsTopic != "" {
			daemon.LifecycleMonitor = &LifecycleMonitor{
				InstanceID:  instanceID,
				Queue:       NewQueue(sess, generateQueueName(instanceID), snsTopic),
				AutoScaling: autoscaling.New(sess),
				Handler:     handler,
			}
		}

		err = daemon.Start(ctx)
		if err != nil {
			log.Error(err)
		}
		return err
	})

	kingpin.MustParse(app.Parse(os.Args[1:]))
}

func getInstanceID() (string, error) {
	res, err := http.Get(metadataURLInstanceID)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Got a %d response from metatadata service", res.StatusCode)
	}

	id, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return string(id), nil
}

func generateQueueName(instanceID string) string {
	return fmt.Sprintf("lifecycled-%s", instanceID)
}
