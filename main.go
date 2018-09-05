package main

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kingpin"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
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
		instanceID   string
		snsTopic     string
		handler      *os.File
		logFile      *os.File
		jsonLogging  bool
		debugLogging bool
	)

	app.Flag("instance-id", "The instance id to listen for events for").
		StringVar(&instanceID)

	app.Flag("sns-topic", "The SNS topic that receives events").
		Required().
		StringVar(&snsTopic)

	app.Flag("handler", "The script to invoke to handle events").
		FileVar(&handler)

	app.Flag("log-file", "Write a copy of the logs to the specified path").
		OpenFileVar(&logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)

	app.Flag("json", "Enable JSON logging").
		BoolVar(&jsonLogging)

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

		if logFile != nil {
			log.SetOutput(io.MultiWriter(os.Stdout, logFile))
		}

		if instanceID == "" {
			log.Infof("Looking up instance id from metadata service")
			id, err := getInstanceID()
			if err != nil {
				log.Fatalf("Failed to lookup instance id: %v", err)
			}
			instanceID = id
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
			InstanceID:  instanceID,
			AutoScaling: autoscaling.New(sess),
			Handler:     handler,
			Signals:     sigs,
			Queue:       NewQueue(sess, generateQueueName(instanceID), snsTopic),
		}

		return daemon.Start(ctx)
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
