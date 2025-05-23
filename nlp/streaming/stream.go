package streaming

import (
	"bufio"
	"io"
)

// ProcessReader calls handler per token or sentence.
func ProcessTokens(r io.Reader, handler func(string)) error {
	scanner := bufio.NewScanner(r)
	scanner.Split(bufio.ScanWords)
	for scanner.Scan() {
		handler(scanner.Text())
	}
	return scanner.Err()
}
