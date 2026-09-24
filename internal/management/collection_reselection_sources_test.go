package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

func reselectionTestSources(t *testing.T, count int, maxRaw int64) (*reselectionSources, *reselectionSpool) {
	t.Helper()
	r, _ := reselectionTestRoot(t)
	spool := reselectionTestSpool(t, r)
	sources, err := newReselectionSources(spool, count, maxRaw)
	if err != nil {
		t.Fatal(err)
	}
	return sources, spool
}

func reselectionAppendPart(t *testing.T, sources *reselectionSources, source, offset uint64, end bool, data []byte) {
	t.Helper()
	if replayed, err := sources.append(t.Context(), source, offset, end, data); err != nil || replayed {
		t.Fatal("new source part not accepted", replayed, err)
	}
}

func TestReselectionSourcesOrderedBoundariesAndEmptySources(t *testing.T) {
	sources, spool := reselectionTestSources(t, 3, reselectionFrameLimit+3)
	if reader, err := sources.reader(t.Context(), 1); err == nil || reader != nil {
		t.Fatal("incomplete source set became readable")
	}
	reselectionAppendPart(t, sources, 1, 0, true, nil)
	if counts, err := spool.accounting(); err != nil || counts.Records != 1 || counts.PlaintextBytes != 0 || counts.EncodedBytes != reselectionFrameHeader+28 {
		t.Fatal("empty source end did not receive authenticated frame", counts, err)
	}
	large := bytes.Repeat([]byte("s"), reselectionFrameLimit)
	reselectionAppendPart(t, sources, 2, 0, false, large)
	reselectionAppendPart(t, sources, 2, uint64(len(large)), true, []byte("end"))
	reselectionAppendPart(t, sources, 3, 0, true, nil)
	state, err := sources.progress()
	if err != nil || state != (reselectionSourcesProgress{SourceCount: 3, SourcesCompleted: 3, RawBytes: reselectionFrameLimit + 3, Complete: true}) {
		t.Fatal("wrong completion state", state, err)
	}
	for source := uint64(1); source <= 3; source++ {
		reader, err := sources.reader(t.Context(), source)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if source == 2 {
			if len(actual) != len(large)+3 || !bytes.Equal(actual[:len(large)], large) || string(actual[len(large):]) != "end" {
				t.Fatal("source frame boundary changed bytes")
			}
		} else if len(actual) != 0 {
			t.Fatal("empty source changed")
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := spool.appendSuffix(t.Context(), 1, []byte("normalized")); err != nil {
		t.Fatal("source readers closed shared spool", err)
	}
}

func TestReselectionSourcesExactLastRetryOnly(t *testing.T) {
	sources, spool := reselectionTestSources(t, 2, 1024)
	reselectionAppendPart(t, sources, 1, 0, false, []byte("secret"))
	before, _ := spool.accounting()
	for range 3 {
		if replayed, err := sources.append(t.Context(), 1, 0, false, []byte("secret")); err != nil || !replayed {
			t.Fatal("exact last part was not replayed", replayed, err)
		}
	}
	for _, part := range []struct {
		source, offset uint64
		end            bool
		data           []byte
	}{
		{1, 0, false, []byte("Secret")}, {1, 0, true, []byte("secret")}, {1, 0, false, []byte("secre")},
		{1, 1, false, []byte("secret")}, {2, 0, true, nil}, {1, 0, false, nil},
	} {
		if replayed, err := sources.append(t.Context(), part.source, part.offset, part.end, part.data); err == nil || replayed {
			t.Fatal("changed/out-of-order part accepted", part.source, part.offset)
		}
	}
	after, _ := spool.accounting()
	if before != after {
		t.Fatal("retry or mismatch appended ciphertext")
	}
	reselectionAppendPart(t, sources, 1, 6, true, nil)
	if replayed, err := sources.append(t.Context(), 1, 6, true, nil); err != nil || !replayed {
		t.Fatal("empty end was not retryable", replayed, err)
	}
	if replayed, err := sources.append(t.Context(), 1, 0, false, []byte("secret")); err == nil || replayed {
		t.Fatal("earlier accepted part became replayable")
	}
	reselectionAppendPart(t, sources, 2, 0, true, []byte("done"))
	frozen, _ := sources.progress()
	if replayed, err := sources.append(t.Context(), 2, 0, true, []byte("done")); err != nil || !replayed {
		t.Fatal("final part was not retryable", replayed, err)
	}
	if replayed, err := sources.append(t.Context(), 2, 4, true, nil); err == nil || replayed {
		t.Fatal("completed source changed")
	}
	if current, _ := sources.progress(); current != frozen {
		t.Fatal("completed replay changed progress")
	}
}

func TestReselectionSourcesLimitsPrecedeGrowth(t *testing.T) {
	sources, spool := reselectionTestSources(t, 1, 3)
	for _, values := range []struct {
		count int
		quota int64
	}{{0, 1}, {1001, 1}, {1, 0}, {1, reselectionRawSourceLimit + 1}} {
		if _, err := newReselectionSources(spool, values.count, values.quota); !errors.Is(err, ErrValidation) {
			t.Fatal("invalid source limits accepted", values, err)
		}
	}
	if _, err := newReselectionSources(spool, 1000, reselectionRawSourceLimit); err != nil {
		t.Fatal("maximum bounded configuration rejected", err)
	}
	for _, part := range []struct {
		source, offset uint64
		end            bool
		data           []byte
	}{
		{0, 0, true, nil}, {2, 0, true, nil}, {1, 0, false, nil}, {1, 0, false, make([]byte, reselectionFrameLimit+1)},
		{1, ^uint64(0), true, nil}, {1, 0, true, []byte("four")},
	} {
		if _, err := sources.append(t.Context(), part.source, part.offset, part.end, part.data); err == nil {
			t.Fatal("invalid or unbounded source part accepted")
		}
	}
	if got, _ := spool.accounting(); got != (reselectionSpoolAccounting{}) || sources.partCount != 0 || len(sources.frames[0]) != 0 {
		t.Fatal("rejection grew staging")
	}
	reselectionAppendPart(t, sources, 1, 0, false, []byte("raw"))
	if _, err := sources.append(t.Context(), 1, 3, false, []byte("x")); !errors.Is(err, errReselectionSpoolQuota) {
		t.Fatal("cumulative raw quota exceeded", err)
	}
	reselectionAppendPart(t, sources, 1, 3, true, nil)
	// The underlying spool counts source and suffix records together. Its
	// exhausted frame allowance must not advance the source-layer index.
	other, limited := reselectionTestSources(t, 1, 128)
	limited.options.MaxRecords = 1
	reselectionAppendPart(t, other, 1, 0, false, []byte("first"))
	if _, err := other.append(t.Context(), 1, 5, true, nil); !errors.Is(err, errReselectionSpoolQuota) {
		t.Fatal("spool record quota ignored", err)
	}
	if other.partCount != 1 || len(other.frames[0]) != 1 || other.state.SourcesCompleted != 0 {
		t.Fatal("failed frame advanced source state")
	}
}

func TestReselectionSourcesRetryAuthenticatesOriginalCiphertext(t *testing.T) {
	sources, spool := reselectionTestSources(t, 1, 128)
	reselectionAppendPart(t, sources, 1, 0, true, []byte("private"))
	ref := sources.last.ref
	if _, err := spool.file.WriteAt([]byte{0xff}, ref.offset+reselectionFrameHeader+12); err != nil {
		t.Fatal(err)
	}
	if replayed, err := sources.append(t.Context(), 1, 0, true, []byte("private")); err == nil || replayed {
		t.Fatal("replay trusted memory/digest instead of original authenticated frame")
	}
}

func TestReselectionSourcesReaderClearsScratch(t *testing.T) {
	for _, mode := range []string{"eof", "close", "cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			sources, spool := reselectionTestSources(t, 1, 128)
			reselectionAppendPart(t, sources, 1, 0, true, []byte("private-plaintext"))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stream, err := sources.reader(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			reader := stream.(*reselectionSourceReader)
			if n, err := reader.Read(make([]byte, 1)); n != 1 || err != nil {
				t.Fatal("initial bounded read failed", n, err)
			}
			borrowed := reader.scratch
			switch mode {
			case "eof":
				if _, err := io.Copy(io.Discard, reader); err != nil {
					t.Fatal(err)
				}
				if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
					t.Fatal("exhausted reader lost EOF", err)
				}
			case "close":
				if err := reader.Close(); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
				if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case "panic":
				func() {
					defer func() {
						if recover() == nil {
							t.Error("parser panic missing")
						}
					}()
					defer reader.Close()
					panic("parser failed")
				}()
			}
			if !bytes.Equal(borrowed, make([]byte, len(borrowed))) || len(reader.scratch) != 0 {
				t.Fatal("reader retained plaintext scratch")
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := spool.appendSuffix(t.Context(), 1, []byte("result")); err != nil {
				t.Fatal("reader owned shared spool", err)
			}
		})
	}
}

func TestReselectionSourcesReaderCorruptionAndCancellation(t *testing.T) {
	sources, spool := reselectionTestSources(t, 1, 128)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := sources.append(ctx, 1, 0, true, []byte("private")); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled upload accepted", err)
	}
	if sources.partCount != 0 {
		t.Fatal("canceled upload changed state")
	}
	reselectionAppendPart(t, sources, 1, 0, false, []byte("first"))
	reselectionAppendPart(t, sources, 1, 5, true, []byte("second"))
	if _, err := sources.reader(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled reader accepted", err)
	}
	stream, err := sources.reader(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	reader := stream.(*reselectionSourceReader)
	if n, err := reader.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	borrowed := reader.scratch
	ref := sources.frames[0][1]
	if _, err := spool.file.WriteAt([]byte{0xee}, ref.offset+reselectionFrameHeader+12); err != nil {
		t.Fatal(err)
	}
	if n, err := reader.Read(make([]byte, 4)); n != 4 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := reader.Read(make([]byte, 128)); n != 0 || err == nil {
		t.Fatal("corrupt frame accepted", n, err)
	}
	if len(reader.scratch) != 0 || !bytes.Equal(borrowed, make([]byte, len(borrowed))) {
		t.Fatal("failed reader retained plaintext")
	}
}

func TestReselectionSourcesConcurrentRetryAndReaders(t *testing.T) {
	sources, spool := reselectionTestSources(t, 1, 1024)
	const workers = 8
	var group sync.WaitGroup
	results := make(chan bool, workers)
	for range workers {
		group.Go(func() {
			replayed, err := sources.append(t.Context(), 1, 0, true, []byte("first\nsecond\n"))
			if err != nil {
				t.Error(err)
			}
			results <- replayed
		})
	}
	group.Wait()
	close(results)
	accepted := 0
	for replayed := range results {
		if !replayed {
			accepted++
		}
	}
	if accepted != 1 || sources.partCount != 1 {
		t.Fatal("concurrent retry created duplicate frames", accepted)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for ordinal := range uint64(workers) {
		group.Go(func() {
			reader, err := sources.reader(ctx, 1)
			if err != nil {
				t.Error(err)
				return
			}
			defer reader.Close()
			buffer := make([]byte, 3)
			for {
				n, err := reader.Read(buffer)
				if n > 0 {
					// A parsing callback runs outside Read, then stages exact
					// normalized output into the same spool.
					if _, err := spool.appendSuffix(ctx, ordinal+1, buffer[:n]); err != nil {
						t.Error(err)
						return
					}
				}
				if errors.Is(err, io.EOF) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	group.Wait()
}

type reselectionObservedContext struct {
	context.Context
	first   *sync.Once
	checked chan struct{}
}

func (c reselectionObservedContext) Err() error {
	err := c.Context.Err()
	c.first.Do(func() { close(c.checked) })
	return err
}

func TestReselectionSourcesCancellationWhileWaitingDoesNotComplete(t *testing.T) {
	for _, waitingOn := range []string{"source", "spool"} {
		t.Run(waitingOn, func(t *testing.T) {
			sources, spool := reselectionTestSources(t, 1, 128)
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := reselectionObservedContext{Context: base, first: &sync.Once{}, checked: make(chan struct{})}
			lock := sources.mu
			if waitingOn == "spool" {
				lock = spool.mu
			}
			lock.Lock()
			result := make(chan error, 1)
			go func() { _, err := sources.append(ctx, 1, 0, true, []byte("private")); result <- err }()
			<-ctx.checked
			cancel()
			lock.Unlock()
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatal("canceled admission completed", err)
			}
			if sources.state.Complete || sources.state.RawBytes != 0 || sources.partCount != 0 {
				t.Fatal("canceled waiter published completion")
			}
		})
	}
}

func TestReselectionSourcesConcurrentReaderClose(t *testing.T) {
	sources, _ := reselectionTestSources(t, 1, reselectionFrameLimit)
	reselectionAppendPart(t, sources, 1, 0, true, bytes.Repeat([]byte("p"), reselectionFrameLimit))
	stream, err := sources.reader(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	reader := stream.(*reselectionSourceReader)
	if _, err := reader.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	borrowed := reader.scratch
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			buffer := make([]byte, 256)
			for range 64 {
				if _, err := reader.Read(buffer); err != nil {
					if !errors.Is(err, errReselectionSources) && !errors.Is(err, io.EOF) {
						t.Error(err)
					}
					return
				}
			}
		})
	}
	group.Go(func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})
	group.Wait()
	if len(reader.scratch) != 0 || !bytes.Equal(borrowed, make([]byte, len(borrowed))) {
		t.Fatal("concurrent Close retained scratch")
	}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, errReselectionSources) {
		t.Fatal("closed reader returned private input", err)
	}
}

func TestReselectionSourcesFormattingDoesNotDiscloseInputs(t *testing.T) {
	sources, spool := reselectionTestSources(t, 1, 128)
	reselectionAppendPart(t, sources, 1, 0, true, []byte("private-source-path-and-secret"))
	stream, err := sources.reader(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	reader := stream.(*reselectionSourceReader)
	if _, err := reader.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{sources, *sources, reader, *reader} {
		text := fmt.Sprintf("%v %+v %#v", value, value, value)
		for _, forbidden := range []string{spool.directory, spool.options.AttemptID, "private-source-path-and-secret"} {
			if bytes.Contains([]byte(text), []byte(forbidden)) {
				t.Fatal("formatter disclosed private input")
			}
		}
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("private source staging serialized")
		}
	}
}
