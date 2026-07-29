package main

import (
	"context"
	"errors"
	"flag"
	"log"
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
	if !accountMatches(*account, aws.ToString(ident.Account)) {
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
		}
		log.Printf("Deleted %d subscriptions, running again as aws limits subscriptions returned to 100", count)
		time.Sleep(time.Second * 2)
	}

	for {
		count, err := deleteInactiveQueues(ctx, sqsClient, ec2Client, *parallel)
		if err != nil {
			fatalAWS(err)
		}

		if count == 0 {
			break
		}
		log.Printf("Deleted %d queues, running again until a pass finds none", count)
		time.Sleep(time.Second * 60)
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

// accountMatches reports whether the resolved account satisfies the --account
// guard. An empty expected means no guard was requested.
func accountMatches(expected, resolved string) bool {
	return expected == "" || expected == resolved
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
// where the last left off. Aborting on any AWS error (rather than skipping the
// offending resource) is deliberate: the SDK already retries transient failures,
// so an error reaching here is unexpected and worth stopping on.
func fatalAWS(err error) {
	if apiErr, expired := expiredCredential(err); expired {
		log.Fatalf("Credentials expired mid-run (%s); refresh them (e.g. `aws sso login`) and run again to resume: %s", apiErr.ErrorCode(), err)
	}
	log.Fatal(err)
}

// expiredCredential returns the AWS API error and true when its code means the
// credentials (temporary STS credentials or the cached SSO token) expired.
func expiredCredential(err error) (smithy.APIError, bool) {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return nil, false
	}
	_, expired := credentialExpiryCodes[apiErr.ErrorCode()]
	return apiErr, expired
}

func deleteInactiveQueues(ctx context.Context, sqsClient *sqs.Client, ec2Client *ec2.Client, parallel int) (uint64, error) {
	queues, err := listInactiveQueues(ctx, sqsClient, ec2Client)
	if err != nil {
		return 0, err
	}
	return deleteQueues(queues, parallel, func(queue string) error {
		return deleteQueue(ctx, sqsClient, queue)
	})
}

// deleteQueues deletes queues using up to parallel workers. It stops at the
// first error, returning 0 and that error; on success it returns the number
// processed. deleteFn is expected to treat an already-deleted queue as success.
func deleteQueues(queues []string, parallel int, deleteFn func(string) error) (uint64, error) {
	if parallel < 1 {
		parallel = 1
	}
	var wg sync.WaitGroup
	var count uint64
	queuesCh := make(chan string)
	// Buffer to the worker count so a worker that errors after the dispatch
	// loop has stopped reading errCh never blocks on the send (goroutine leak).
	errCh := make(chan error, parallel)

	wg.Add(parallel)
	for i := 0; i < parallel; i++ {
		go func() {
			defer wg.Done()
			for queue := range queuesCh {
				n := atomic.AddUint64(&count, 1)
				log.Printf("Deleting %s (%d of %d)", queue, n, len(queues))
				if err := deleteFn(queue); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}

	// dispatch work, stopping as soon as a worker reports an error. The
	// pre-send check keeps a buffered error from losing the select race to a
	// ready send, which would otherwise dispatch more work after the failure.
	var err error
	for _, queue := range queues {
		select {
		case err = <-errCh:
		default:
		}
		if err != nil {
			break
		}
		select {
		case queuesCh <- queue:
		case err = <-errCh:
		}
		if err != nil {
			break
		}
	}
	close(queuesCh)
	wg.Wait()

	if err == nil {
		// Surface an error from a worker that finished after dispatch ended.
		select {
		case err = <-errCh:
		default:
		}
	}
	if err != nil {
		return 0, err
	}
	return count, nil
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

// SQSClient is the subset of the SQS client the queue cleanup uses, so the
// listing and delete logic can be exercised with a fake in tests.
type SQSClient interface {
	ListQueues(context.Context, *sqs.ListQueuesInput, ...func(*sqs.Options)) (*sqs.ListQueuesOutput, error)
	DeleteQueue(context.Context, *sqs.DeleteQueueInput, ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error)
}

func listQueues(ctx context.Context, client SQSClient) ([]string, error) {
	var urls []string
	// MaxResults is required for SQS to return a NextToken; without it the
	// response caps at 1000 URLs and the paginator stops after one page.
	paginator := sqs.NewListQueuesPaginator(client, &sqs.ListQueuesInput{
		QueueNamePrefix: aws.String(`lifecycled-`),
		MaxResults:      aws.Int32(1000),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		urls = append(urls, page.QueueUrls...)
	}
	return urls, nil
}

func listInactiveQueues(ctx context.Context, sqsClient *sqs.Client, ec2Client *ec2.Client) ([]string, error) {
	instances, err := listInstances(ctx, ec2Client)
	if err != nil {
		return nil, err
	}

	log.Printf("Found %d running instances", len(instances))

	// build a map for quick lookups
	var instancesMap = map[string]struct{}{}
	for _, instance := range instances {
		instancesMap[instance] = struct{}{}
	}

	queues, err := listQueues(ctx, sqsClient)
	if err != nil {
		return nil, err
	}

	log.Printf("Found %d lifecycled queues total", len(queues))

	inactiveQueues := filterInactiveQueues(queues, instancesMap)

	log.Printf("Found %d inactive queues", len(inactiveQueues))

	return inactiveQueues, nil
}

// instanceIDFromQueueURL returns the EC2 instance id encoded in a lifecycled
// queue URL (.../<account>/lifecycled-<instanceID>) and whether the URL follows
// that scheme. It reads the queue name from the final path segment, so it works
// across every SQS endpoint (standard, GovCloud, China, FIPS, dualstack) without
// matching the hostname.
func instanceIDFromQueueURL(queueURL string) (string, bool) {
	name := queueURL[strings.LastIndex(queueURL, "/")+1:]
	id, ok := strings.CutPrefix(name, "lifecycled-")
	if !ok {
		return "", false
	}
	if rest, ok := strings.CutPrefix(id, "i-"); !ok || rest == "" {
		return "", false
	}
	return id, true
}

// filterInactiveQueues returns the lifecycled- queue URLs whose instance id is
// not in running. URLs that don't follow the lifecycled- naming scheme are ignored.
func filterInactiveQueues(urls []string, running map[string]struct{}) []string {
	var inactive []string
	for _, queue := range urls {
		instanceID, ok := instanceIDFromQueueURL(queue)
		if !ok {
			continue
		}
		if _, exists := running[instanceID]; !exists {
			inactive = append(inactive, queue)
		}
	}
	return inactive
}

func deleteQueue(ctx context.Context, client SQSClient, queueURL string) error {
	_, err := client.DeleteQueue(ctx, &sqs.DeleteQueueInput{
		QueueUrl: aws.String(queueURL),
	})
	var notExist *sqstypes.QueueDoesNotExist
	if errors.As(err, &notExist) {
		// Already gone; deleting a non-existent queue is success.
		return nil
	}
	return err
}

// SNSClient is the subset of the SNS client the subscription cleanup uses, so the
// cleanup logic can be exercised with a fake in tests.
type SNSClient interface {
	ListSubscriptions(context.Context, *sns.ListSubscriptionsInput, ...func(*sns.Options)) (*sns.ListSubscriptionsOutput, error)
	GetTopicAttributes(context.Context, *sns.GetTopicAttributesInput, ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error)
	Unsubscribe(context.Context, *sns.UnsubscribeInput, ...func(*sns.Options)) (*sns.UnsubscribeOutput, error)
}

func topicExists(ctx context.Context, client SNSClient, snsTopic string) (bool, error) {
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

func listInactiveSubscriptions(ctx context.Context, client SNSClient) ([]string, error) {
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
			exists, err := topicExists(ctx, client, topicArn)
			if err != nil {
				// A non-NotFound lookup failure (throttling, AccessDenied, expired
				// creds) must not be read as "topic gone": abort rather than risk
				// unsubscribing live subscriptions.
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

func deleteInactiveSubscriptions(ctx context.Context, client SNSClient) (int, error) {
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
