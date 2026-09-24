package cli

import (
	"context"
	"io"
)

// diffStdinReader is only installed for process-owned os.Stdin. Closing a native
// blocking descriptor does not always interrupt an outstanding read. On cancel,
// leave at most one such read until native main exits, so Freeze can remove its
// private spool first. The read owns its buffer and cannot touch a returned caller
// buffer. Readers injected by library callers retain their own lifecycle contract.
type diffStdinReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r diffStdinReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	type result struct {
		data []byte
		err  error
	}
	ready := make(chan result)
	size := len(p)
	go func() {
		buffer := make([]byte, size)
		n, err := r.reader.Read(buffer)
		select {
		case ready <- result{data: buffer[:n], err: err}:
			// The receiving Read now owns and clears this buffer.
		case <-r.ctx.Done():
			clear(buffer)
		}
	}()
	select {
	case value := <-ready:
		n := copy(p, value.data)
		clear(value.data)
		return n, value.err
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	}
}
