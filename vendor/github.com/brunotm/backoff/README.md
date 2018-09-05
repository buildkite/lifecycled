# backoff
### backoff provides a simple backoff and jitter implementation for retrying operations
### As described in:
 - https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/

### Bounded retries:
```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/brunotm/backoff"
)

func main() {
	count := 0
	err := backoff.Retry(
		context.Background(), 100, 1*time.Second, 60*time.Second,
		func() error {
			count++
			fmt.Println("Count: ", count)
			if count == 5 {
				return nil
			}
			return fmt.Errorf("op error")
		})
	fmt.Println(err)
}
```

### Unbounded retries:
```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/brunotm/backoff"
)

func main() {
	count := 0
	// Until only returns an error when the context is done.
	err := backoff.Until(
		context.Background(), 1*time.Second, 60*time.Second,
		func() error {
			count++
			fmt.Println("Count: ", count)
			if count == 5 {
				return nil
			}
			return fmt.Errorf("op error")
		})
	fmt.Println(err)
}
```


Written by Bruno Moura <brunotm@gmail.com>
