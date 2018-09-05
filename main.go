package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/brunotm/backoff"
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
	client := http.Client{
		Timeout: time.Second * 5,
	}

	instanceIDCh := make(chan string)
	defer close(instanceIDCh)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	// Retry getting the instance id. Sometimes the metadata service takes a while to start
	err := backoff.Until(ctx, time.Second, time.Second*5, func() error {
		res, err := client.Get(metadataURLInstanceID)
		if err != nil {
			log.WithError(err).Errorf("Failed to read instance-id from metadata service")
			return err
		}
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			err := fmt.Errorf("Got a %d response from metatadata service", res.StatusCode)
			log.WithError(err).Errorf("Failed to read instance-id from metadata service")
			return err
		}

		id, err := ioutil.ReadAll(res.Body)
		if err != nil {
			log.WithError(err).Errorf("Failed to read instance-id from metadata service: %v", err)
			return err
		}

		instanceIDCh <- string(id)
		return nil
	})

	if err != nil {
		return "", err
	}

	return <-instanceIDCh, nil
}

func generateQueueName(instanceID string) string {
	return fmt.Sprintf("lifecycled-%s", instanceID)
}
