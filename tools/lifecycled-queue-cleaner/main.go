package main

import (
	"flag"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/sns"
	"github.com/aws/aws-sdk-go/service/sqs"
)

func main() {
	parallel := flag.Int("parallel", 20, "The number of parallel deletes to run")
	flag.Parse()

	for {
		count, err := deleteInactiveSubscriptions(session.New())
		if err != nil {
			log.Fatal(err)
		}

		if count == 0 {
			break
		} else {
			log.Printf("Deleted %d subscriptions, running again as aws limits subscriptions returned to 100", count)
			time.Sleep(time.Second * 2)
		}
	}

	for {
		count, err := deleteInactiveQueues(session.New(), *parallel)
		if err != nil {
			log.Fatal(err)
		}

		if count == 0 {
			break
		} else {
			log.Printf("Deleted %d queues, running again as aws limits queues returned to 1000", count)
			time.Sleep(time.Second * 60)
		}
	}

	log.Printf("Done! Sorry for the inconvenience!")
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
				atomic.AddUint64(&count, 1)
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
			{
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

func topicExists(sess *session.Session, snsTopic string) (bool, error) {
	_, err := sns.New(sess).GetTopicAttributes(&sns.GetTopicAttributesInput{
		TopicArn: aws.String(snsTopic),
	})
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok {
			if awsErr.Code() == `NotFound` {
				return false, nil
			}
		}
		log.Printf("%#v", err.Error())
		return false, err
	}
	return true, nil
}

func listInactiveSubscriptions(sess *session.Session) ([]string, error) {
	var subs []string
	var topics = make(map[string]bool, 0)
	var count int

	err := sns.New(sess).ListSubscriptionsPages(&sns.ListSubscriptionsInput{},
		func(page *sns.ListSubscriptionsOutput, lastPage bool) bool {
			count = count + len(page.Subscriptions)
			for _, s := range page.Subscriptions {
				if !strings.Contains(*s.Endpoint, "lifecycled-i") {
					continue
				}
				if exists, ok := topics[*s.TopicArn]; ok {
					if !exists {
						subs = append(subs, *s.SubscriptionArn)
					}
					continue
				}
				if exists, _ := topicExists(sess, *s.TopicArn); exists {
					topics[*s.TopicArn] = true
				} else {
					topics[*s.TopicArn] = false
					subs = append(subs, *s.SubscriptionArn)
				}
			}
			return lastPage
		})
	if err != nil {
		return nil, err
	}

	log.Printf("Found %d sns subscriptions in total", count)
	return subs, nil
}

func deleteInactiveSubscriptions(sess *session.Session) (int, error) {
	subs, err := listInactiveSubscriptions(sess)
	if err != nil {
		return 0, err
	}
	log.Printf("Found %d inactive subscriptions", len(subs))
	var deleted int
	for idx, s := range subs {
		log.Printf("Deleting sns subscription %s (%d of %d)", s, idx+1, len(subs))
		_, err := sns.New(sess).Unsubscribe(&sns.UnsubscribeInput{
			SubscriptionArn: aws.String(s),
		})
		if err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}
