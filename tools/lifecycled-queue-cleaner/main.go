package main

import (
	"flag"
	"log"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/sqs"
)

func main() {
	parallel := flag.Int("parallel", 20, "The number of parallel deletes to run")
	flag.Parse()

	for {
		count, err := deleteInactiveQueues(session.New(), *parallel)
		if err != nil {
			log.Fatal(err)
		}

		if count == 0 {
			log.Printf("Done!")
			return
		} else {
			log.Printf("Deleted %d queues, running again as aws limits queues returned to 1000", count)
			time.Sleep(time.Second * 5)
		}
	}
}

func deleteInactiveQueues(sess *session.Session, parallel int) (uint64, error) {
	queues, err := listInactiveQueues(sess)
	if err != nil {
		log.Fatal(err)
	}

	var wg sync.WaitGroup
	var count uint64
	var queuesCh = make(chan string)
	var errCh = make(chan error)

	// spawn parallel workers
	for i := 0; i < parallel; i++ {
		go func(total int) {
			for queue := range queuesCh {
				log.Printf("Deleting %s (%d of %d)", queue, count, total)
				err = deleteQueue(sess, queue)
				wg.Done()
				if awsErr, ok := err.(awserr.Error); ok {
					if awsErr.Code() == `AWS.SimpleQueueService.NonExistentQueue` {
						continue
					}
				}
				if err != nil {
					errCh <- err
					return
				}
				atomic.AddUint64(&count, 1)
			}
		}(len(queues))
	}

	// dispatch work to parallel workers
	for _, queue := range queues {
		select {
		case queuesCh <- queue:
			wg.Add(1)
		case err := <-errCh:
			close(queuesCh)
			return 0, err
		}
	}

	// wait for work to finish
	wg.Wait()
	close(queuesCh)

	return count, nil
}

func listInstances(sess *session.Session) ([]string, error) {
	var instances []string

	// Only grab instances that are running or just started
	params := &ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			&ec2.Filter{
				Name:   aws.String(`instance-state-name`),
				Values: aws.StringSlice([]string{"running", "pending"}),
			},
		},
	}

	err := ec2.New(sess).DescribeInstancesPages(params,
		func(page *ec2.DescribeInstancesOutput, lastPage bool) bool {
			for _, reservation := range page.Reservations {
				for _, instance := range reservation.Instances {
					instances = append(instances, *instance.InstanceId)
				}
			}
			return lastPage
		})
	if err != nil {
		return nil, err
	}

	return instances, nil
}

func listQueues(sess *session.Session) ([]string, error) {
	resp, err := sqs.New(sess).ListQueues(&sqs.ListQueuesInput{
		QueueNamePrefix: aws.String(`lifecycled-`),
	})
	if err != nil {
		return nil, err
	}

	var queues = make([]string, len(resp.QueueUrls))
	for idx, queue := range resp.QueueUrls {
		queues[idx] = *queue
	}

	return queues, nil
}

var queueRegex = regexp.MustCompile(`^https://sqs\.(.+?)\.amazonaws.com/(.+?)/lifecycled-(i-.+)$`)

func listInactiveQueues(sess *session.Session) ([]string, error) {
	instances, err := listInstances(sess)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Found %d running instances", len(instances))

	// build a map for quick lookups
	var instancesMap = map[string]struct{}{}
	for _, instance := range instances {
		instancesMap[instance] = struct{}{}
	}

	queues, err := listQueues(sess)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Found %d queues total (aws returns max 1000)", len(queues))

	var inactiveQueues []string
	for _, queue := range queues {
		matches := queueRegex.FindStringSubmatch(queue)
		if len(matches) != 4 {
			continue
		}
		instanceId := matches[3]
		if _, exists := instancesMap[instanceId]; !exists {
			inactiveQueues = append(inactiveQueues, queue)
		}
	}

	log.Printf("Found %d inactive queues", len(inactiveQueues))

	return inactiveQueues, nil

}

func deleteQueue(sess *session.Session, queueUrl string) error {
	_, err := sqs.New(sess).DeleteQueue(&sqs.DeleteQueueInput{
		QueueUrl: aws.String(queueUrl),
	})
	return err
}
