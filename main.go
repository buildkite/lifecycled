package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	log "github.com/Sirupsen/logrus"
	"github.com/alecthomas/kingpin"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
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

		var (
			cleanQueue        sync.Once
			cleanSubscription sync.Once
		)
		queueName := generateQueueName(instanceID)

		queue := NewQueue(sess, queueName, snsTopic)
		if err := queue.Create(); err != nil {
			log.WithError(err).Fatal("Failed to create queue")
		}
		deleteQueue := func() {
			if err = queue.Delete(); err != nil {
				log.WithError(err).Fatal("Failed to delete queue")
			}
		}
		defer cleanQueue.Do(deleteQueue)

		if err := queue.Subscribe(); err != nil {
			// Cannot log fatal here since the deferred calls will not be run (leaks a queue)
			log.WithError(err).Error("Failed to subscribe to the sns topic")
			return err
		}
		deleteSubscription := func() {
			if err := queue.Unsubscribe(); err != nil {
				log.WithError(err).Error("Failed to unsubscribe from sns topic")
			}

		}
		defer cleanSubscription.Do(deleteSubscription)

		sigs := make(chan os.Signal, 2)
		signal.Notify(sigs,
			syscall.SIGHUP,
			syscall.SIGINT,
			syscall.SIGTERM,
			syscall.SIGQUIT,
			syscall.SIGPIPE)

		go func() {
			<-sigs
			log.Info("Shutting down gracefully...")
			cleanSubscription.Do(deleteSubscription)
			cleanQueue.Do(deleteQueue)
			os.Exit(1)
		}()

		daemon := Daemon{
			InstanceID:  instanceID,
			AutoScaling: autoscaling.New(sess),
			Handler:     handler,
			Signals:     sigs,
			Queue:       queue,
		}

		return daemon.Start()
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
