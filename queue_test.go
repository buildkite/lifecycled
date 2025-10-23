package lifecycled

import (
	"testing"

	"github.com/aws/aws-sdk-go/aws"
)

func TestParseTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]*string
	}{
		{
			name:     "empty string returns nil",
			input:    "",
			expected: nil,
		},
		{
			name:  "single key-value pair",
			input: "key1=value1",
			expected: map[string]*string{
				"key1": aws.String("value1"),
			},
		},
		{
			name:  "multiple key-value pairs",
			input: "key1=value1,key2=value2,key3=value3",
			expected: map[string]*string{
				"key1": aws.String("value1"),
				"key2": aws.String("value2"),
				"key3": aws.String("value3"),
			},
		},
		{
			name:  "pairs with whitespace",
			input: " key1 = value1 , key2 = value2 ",
			expected: map[string]*string{
				"key1": aws.String("value1"),
				"key2": aws.String("value2"),
			},
		},
		{
			name:  "value with equals sign",
			input: "key1=value=with=equals",
			expected: map[string]*string{
				"key1": aws.String("value=with=equals"),
			},
		},
		{
			name:     "pair without equals sign",
			input:    "key1",
			expected: map[string]*string{},
		},
		{
			name:  "empty value",
			input: "key1=",
			expected: map[string]*string{
				"key1": aws.String(""),
			},
		},
		{
			name:     "empty key ignored",
			input:    "=value1",
			expected: map[string]*string{},
		},
		{
			name:  "mixed valid and invalid pairs",
			input: "key1=value1,invalid,key2=value2",
			expected: map[string]*string{
				"key1": aws.String("value1"),
				"key2": aws.String("value2"),
			},
		},
		{
			name:     "whitespace only key ignored",
			input:    "   =value1",
			expected: map[string]*string{},
		},
		{
			name:  "special characters in value",
			input: "key1=value-with_special.chars",
			expected: map[string]*string{
				"key1": aws.String("value-with_special.chars"),
			},
		},
		{
			name:  "comma in value not supported",
			input: "key1=alpha,beta,key2=gamma",
			expected: map[string]*string{
				"key1": aws.String("alpha"),
				"key2": aws.String("gamma"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseTags(tt.input)

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
				if aws.StringValue(actualValue) != aws.StringValue(expectedValue) {
					t.Errorf("for key %q: expected %q, got %q",
						key, aws.StringValue(expectedValue), aws.StringValue(actualValue))
				}
			}
		})
	}
}
