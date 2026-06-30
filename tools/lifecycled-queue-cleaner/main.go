package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

func main() {
	parallel := flag.Int("parallel", 20, "The number of parallel deletes to run")
	account := flag.String("account", "", "If set, abort unless the resolved AWS account ID matches")
	flag.Parse()

	ctx := context.Background()

	// LoadDefaultConfig loads ~/.aws/config so region, named profiles, and SSO
	// resolve. WithEC2IMDSRegion falls back to instance metadata when running on EC2.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithEC2IMDSRegion())
	if err != nil {
		log.Fatalf("Failed to load aws config: %s", err)
	}
	if cfg.Region == "" {
		log.Fatal("No region resolved; set AWS_REGION, AWS_DEFAULT_REGION, or a profile region")
	}
	log.Printf("Using region %s", cfg.Region)

	sqsClient := sqs.NewFromConfig(cfg)
	snsClient := sns.NewFromConfig(cfg)
	ec2Client := ec2.NewFromConfig(cfg)

	// Confirm the target account before any destructive calls. A failure means the
	// credentials are unusable, since GetCallerIdentity needs no IAM permission.
	ident, err := callerIdentity(ctx, sts.NewFromConfig(cfg))
	if err != nil {
		log.Fatalf("Failed to resolve caller identity: %s", err)
	}
	if *account != "" && *account != aws.ToString(ident.Account) {
		log.Fatalf("Resolved account %s does not match the expected account %s", aws.ToString(ident.Account), *account)
	}
	log.Printf("Using account %s as %s", aws.ToString(ident.Account), aws.ToString(ident.Arn))

	for {
		count, err := deleteInactiveSubscriptions(ctx, snsClient)
		if err != nil {
			fatalAWS(err)
		}

		if count == 0 {
			break
		} else {
			log.Printf("Deleted %d subscriptions, running again as aws limits subscriptions returned to 100", count)
			time.Sleep(time.Second * 2)
		}
	}

	for {
		count, err := deleteInactiveQueues(ctx, sqsClient, ec2Client, *parallel)
		if err != nil {
			fatalAWS(err)
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

// callerIdentity resolves the account the credentials belong to, bounded so a
// hung STS fails fast instead of stalling startup.
func callerIdentity(ctx context.Context, client *sts.Client) (*sts.GetCallerIdentityOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
}

// credentialExpiryCodes are the AWS error codes meaning the credentials
// (temporary STS credentials or the cached SSO token) expired and need a refresh.
var credentialExpiryCodes = map[string]struct{}{
	`ExpiredToken`:            {},
	`ExpiredTokenException`:   {},
	`RequestExpired`:          {},
	`SSOProviderInvalidToken`: {},
}

// fatalAWS ends the run, adding a re-auth hint when the failure is expired
// credentials. Re-running is safe: each run re-lists from scratch and resumes
// where the last left off.
func fatalAWS(err error) {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if _, expired := credentialExpiryCodes[apiErr.ErrorCode()]; expired {
			log.Fatalf("Credentials expired mid-run (%s); refresh them (e.g. `aws sso login`) and run again to resume: %s", apiErr.ErrorCode(), err)
		}
	}
	log.Fatal(err)
}

func deleteInactiveQueues(ctx context.Context, sqsClient *sqs.Client, ec2Client *ec2.Client, parallel int) (uint64, error) {
	queues, err := listInactiveQueues(ctx, sqsClient, ec2Client)
	if err != nil {
		fatalAWS(err)
	}

	var wg sync.WaitGroup
	var count uint64
	var queuesCh = make(chan string)
	// Buffered so a worker that fails after the dispatch loop has stopped reading
	// errCh can still report its error instead of blocking (and leaking) forever.
	var errCh = make(chan error, parallel)

	// spawn parallel workers
	for i := 0; i < parallel; i++ {
		go func(total int) {
			for queue := range queuesCh {
				atomic.AddUint64(&count, 1)
				log.Printf("Deleting %s (%d of %d)", queue, count, total)
				err := deleteQueue(ctx, sqsClient, queue)
				wg.Done()
				var notExist *sqstypes.QueueDoesNotExist
				if errors.As(err, &notExist) {
					continue
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
		// Count the work before handing it off so a worker can't call wg.Done
		// before the matching wg.Add and drive the counter negative.
		wg.Add(1)
		select {
		case queuesCh <- queue:
		case err := <-errCh:
			close(queuesCh)
			return 0, err
		}
	}

	// wait for work to finish
	wg.Wait()
	close(queuesCh)

	// Surface an error from a worker that failed after dispatch finished.
	select {
	case err := <-errCh:
		return count, err
	default:
		return count, nil
	}
}

func listInstances(ctx context.Context, client *ec2.Client) ([]string, error) {
	var instances []string

	// Only grab instances that are running or just started
	params := &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String(`instance-state-name`),
				Values: []string{"running", "pending"},
			},
		},
	}

	paginator := ec2.NewDescribeInstancesPaginator(client, params)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, reservation := range page.Reservations {
			for _, instance := range reservation.Instances {
				instances = append(instances, aws.ToString(instance.InstanceId))
			}
		}
	}

	return instances, nil
}

func listQueues(ctx context.Context, client *sqs.Client) ([]string, error) {
	resp, err := client.ListQueues(ctx, &sqs.ListQueuesInput{
		QueueNamePrefix: aws.String(`lifecycled-`),
	})
	if err != nil {
		return nil, err
	}
	return resp.QueueUrls, nil
}

var queueRegex = regexp.MustCompile(`^https://sqs\.(.+?)\.amazonaws.com/(.+?)/lifecycled-(i-.+)$`)

func listInactiveQueues(ctx context.Context, sqsClient *sqs.Client, ec2Client *ec2.Client) ([]string, error) {
	instances, err := listInstances(ctx, ec2Client)
	if err != nil {
		fatalAWS(err)
	}

	log.Printf("Found %d running instances", len(instances))

	// build a map for quick lookups
	var instancesMap = map[string]struct{}{}
	for _, instance := range instances {
		instancesMap[instance] = struct{}{}
	}

	queues, err := listQueues(ctx, sqsClient)
	if err != nil {
		fatalAWS(err)
	}

	log.Printf("Found %d queues total (aws returns max 1000)", len(queues))

	inactiveQueues := filterInactiveQueues(queues, instancesMap)

	log.Printf("Found %d inactive queues", len(inactiveQueues))

	return inactiveQueues, nil
}

// filterInactiveQueues returns the lifecycled- queue URLs whose instance id is
// not in running. URLs that don't match the lifecycled- naming scheme are skipped.
func filterInactiveQueues(urls []string, running map[string]struct{}) []string {
	var inactive []string
	for _, queue := range urls {
		matches := queueRegex.FindStringSubmatch(queue)
		if len(matches) != 4 {
			continue
		}
		instanceID := matches[3]
		if _, exists := running[instanceID]; !exists {
			inactive = append(inactive, queue)
		}
	}
	return inactive
}

func deleteQueue(ctx context.Context, client *sqs.Client, queueURL string) error {
	_, err := client.DeleteQueue(ctx, &sqs.DeleteQueueInput{
		QueueUrl: aws.String(queueURL),
	})
	return err
}

// snsCleanupClient is the subset of the SNS API the subscription cleanup uses,
// so the logic can be exercised with a fake in tests.
type snsCleanupClient interface {
	ListSubscriptions(context.Context, *sns.ListSubscriptionsInput, ...func(*sns.Options)) (*sns.ListSubscriptionsOutput, error)
	GetTopicAttributes(context.Context, *sns.GetTopicAttributesInput, ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error)
	Unsubscribe(context.Context, *sns.UnsubscribeInput, ...func(*sns.Options)) (*sns.UnsubscribeOutput, error)
}

func topicExists(ctx context.Context, client snsCleanupClient, snsTopic string) (bool, error) {
	_, err := client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{
		TopicArn: aws.String(snsTopic),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == `NotFound` {
			return false, nil
		}
		log.Printf("Failed to get topic attributes: %s", err)
		return false, err
	}
	return true, nil
}

func listInactiveSubscriptions(ctx context.Context, client snsCleanupClient) ([]string, error) {
	var subs []string
	var topics = map[string]bool{}
	var count int

	paginator := sns.NewListSubscriptionsPaginator(client, &sns.ListSubscriptionsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		count = count + len(page.Subscriptions)
		for _, s := range page.Subscriptions {
			if !strings.Contains(aws.ToString(s.Endpoint), "lifecycled-i") {
				continue
			}
			topicArn := aws.ToString(s.TopicArn)
			if exists, ok := topics[topicArn]; ok {
				if !exists {
					subs = append(subs, aws.ToString(s.SubscriptionArn))
				}
				continue
			}
			// A non-NotFound failure (AccessDenied, throttling, expired creds) must
			// abort rather than be treated as a missing topic, or we would delete
			// live subscriptions.
			exists, err := topicExists(ctx, client, topicArn)
			if err != nil {
				return nil, err
			}
			if exists {
				topics[topicArn] = true
			} else {
				topics[topicArn] = false
				subs = append(subs, aws.ToString(s.SubscriptionArn))
			}
		}
	}

	log.Printf("Found %d sns subscriptions in total", count)
	return subs, nil
}

func deleteInactiveSubscriptions(ctx context.Context, client *sns.Client) (int, error) {
	subs, err := listInactiveSubscriptions(ctx, client)
	if err != nil {
		return 0, err
	}
	log.Printf("Found %d inactive subscriptions", len(subs))
	var deleted int
	for idx, s := range subs {
		log.Printf("Deleting sns subscription %s (%d of %d)", s, idx+1, len(subs))
		_, err := client.Unsubscribe(ctx, &sns.UnsubscribeInput{
			SubscriptionArn: aws.String(s),
		})
		if err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}
