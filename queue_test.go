package lifecycled

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func TestParseTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
		wantErr  bool
	}{
		{
			name:     "empty string returns nil",
			input:    "",
			expected: nil,
		},
		{
			name:  "single key-value pair",
			input: "key1=value1",
			expected: map[string]string{
				"key1": "value1",
			},
		},
		{
			name:  "multiple key-value pairs",
			input: "key1=value1,key2=value2,key3=value3",
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
				"key3": "value3",
			},
		},
		{
			name:  "pairs with whitespace",
			input: " key1 = value1 , key2 = value2 ",
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name:  "value with equals sign",
			input: "key1=value=with=equals",
			expected: map[string]string{
				"key1": "value=with=equals",
			},
		},
		{
			name:     "pair without equals sign",
			input:    "key1",
			expected: map[string]string{},
		},
		{
			name:  "empty value",
			input: "key1=",
			expected: map[string]string{
				"key1": "",
			},
		},
		{
			name:     "empty key ignored",
			input:    "=value1",
			expected: map[string]string{},
		},
		{
			name:  "mixed valid and invalid pairs",
			input: "key1=value1,invalid,key2=value2",
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name:     "whitespace only key ignored",
			input:    "   =value1",
			expected: map[string]string{},
		},
		{
			name:  "special characters in value",
			input: "key1=value-with_special.chars",
			expected: map[string]string{
				"key1": "value-with_special.chars",
			},
		},
		{
			name:  "comma in value not supported",
			input: "key1=alpha,beta,key2=gamma",
			expected: map[string]string{
				"key1": "alpha",
				"key2": "gamma",
			},
		},
		// AWS-specific restriction tests
		{
			name:    "key exceeds 128 characters",
			input:   strings.Repeat("a", 129) + "=value1",
			wantErr: true,
		},
		{
			name:    "value exceeds 256 characters",
			input:   "key1=" + strings.Repeat("a", 257),
			wantErr: true,
		},
		{
			name:  "key at max length (128 chars)",
			input: strings.Repeat("a", 128) + "=value1",
			expected: map[string]string{
				strings.Repeat("a", 128): "value1",
			},
		},
		{
			name:  "value at max length (256 chars)",
			input: "key1=" + strings.Repeat("a", 256),
			expected: map[string]string{
				"key1": strings.Repeat("a", 256),
			},
		},
		{
			name:    "exceeds 50 tags limit",
			input:   generateTagString(51),
			wantErr: true,
		},
		{
			name:     "at 50 tags limit",
			input:    generateTagString(50),
			expected: generateTagMap(50),
		},
		{
			name:    "key starts with aws: prefix",
			input:   "aws:something=value1",
			wantErr: true,
		},
		{
			name:    "key starts with AWS: prefix (case insensitive)",
			input:   "AWS:something=value1",
			wantErr: true,
		},
		{
			name:  "key contains aws but doesn't start with aws:",
			input: "myaws:key=value1",
			expected: map[string]string{
				"myaws:key": "value1",
			},
		},
		{
			name:  "valid special characters in key",
			input: "key-name_123.test:value=myvalue",
			expected: map[string]string{
				"key-name_123.test:value": "myvalue",
			},
		},
		{
			name:  "spaces in key and value allowed",
			input: "my key=my value",
			expected: map[string]string{
				"my key": "my value",
			},
		},
		{
			name:  "plus and equals in value",
			input: "key1=value+with=signs",
			expected: map[string]string{
				"key1": "value+with=signs",
			},
		},
		{
			name:  "duplicate keys - last one wins",
			input: "key1=value1,key1=value2",
			expected: map[string]string{
				"key1": "value2",
			},
		},
		{
			name:  "unicode characters in tags",
			input: "key1=值,key2=значение",
			expected: map[string]string{
				"key1": "值",
				"key2": "значение",
			},
		},
		{
			name:     "key with only whitespace after trim",
			input:    "   =value",
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseTags(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("expected %d tags, got %d", len(tt.expected), len(result))
				return
			}

			for key, expectedValue := range tt.expected {
				actualValue, ok := result[key]
				if !ok {
					t.Errorf("expected key %q not found in result", key)
					continue
				}
				if actualValue != expectedValue {
					t.Errorf("for key %q: expected %q, got %q", key, expectedValue, actualValue)
				}
			}
		})
	}
}

// Helper function to generate a tag string with n tags
func generateTagString(n int) string {
	var parts []string
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("key%d=value%d", i, i))
	}
	return strings.Join(parts, ",")
}

// Helper function to generate expected tag map with n tags
func generateTagMap(n int) map[string]string {
	result := make(map[string]string)
	for i := 0; i < n; i++ {
		result[fmt.Sprintf("key%d", i)] = fmt.Sprintf("value%d", i)
	}
	return result
}

// GetMessages swallows context cancellation (the daemon is shutting down) but
// propagates every other error.
func TestQueueGetMessages(t *testing.T) {
	sentinel := errors.New("boom")

	tests := []struct {
		name       string
		receiveErr error
		wantErr    error // nil means GetMessages should return nil, nil
	}{
		{"context canceled is swallowed", fmt.Errorf("receive: %w", context.Canceled), nil},
		{"deadline exceeded is swallowed", fmt.Errorf("receive: %w", context.DeadlineExceeded), nil},
		{"other errors propagate", sentinel, sentinel},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := NewQueue("queue", "topic", &stubSQSClient{receiveErr: tc.receiveErr}, &stubSNSClient{}, "")
			msgs, err := q.GetMessages(context.Background())

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("GetMessages returned error: %v", err)
				}
				if msgs != nil {
					t.Errorf("expected nil messages, got %v", msgs)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want it to wrap %v", err, tc.wantErr)
			}
		})
	}
}

// Delete treats a missing queue as success (that is the desired end state) but
// propagates every other error.
func TestQueueDelete(t *testing.T) {
	sentinel := errors.New("boom")

	tests := []struct {
		name           string
		deleteQueueErr error
		wantErr        error // nil means Delete should return nil
	}{
		{"queue does not exist is swallowed", &sqstypes.QueueDoesNotExist{}, nil},
		{"other errors propagate", sentinel, sentinel},
		{"success", nil, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := NewQueue("queue", "topic", &stubSQSClient{deleteQueueErr: tc.deleteQueueErr}, &stubSNSClient{}, "")
			err := q.Delete(context.Background())

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Delete returned error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want it to wrap %v", err, tc.wantErr)
			}
		})
	}
}
