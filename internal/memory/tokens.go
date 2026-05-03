package memory

import (
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

var (
	encOnce sync.Once
	enc     *tiktoken.Tiktoken
	encErr  error
)

func encoder() (*tiktoken.Tiktoken, error) {
	encOnce.Do(func() {
		enc, encErr = tiktoken.GetEncoding("cl100k_base")
	})
	return enc, encErr
}

// Estimate returns an approximate token count for s. Falls back to byte/4 if
// the tokenizer cannot be loaded.
func Estimate(s string) int {
	e, err := encoder()
	if err != nil || e == nil {
		return len(s) / 4
	}
	return len(e.Encode(s, nil, nil))
}
