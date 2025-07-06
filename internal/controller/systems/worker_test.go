package systems

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"testing"
	"text/tabwriter"
	"time"
)

//func TestSimpleWorker(t *testing.T) {
//	fmt.Println("Starting test")
//	s := make(chan job[int])
//	r := make(chan response[int])
//	fan := make(chan response[int])
//	w := testWorker[int]{}
//	go w.Start(s, r)
//
//	for i := 0; i < 10; i++ {
//		s <- &testJob{x: i, y: rand.Intn(10)}
//	}
//	for {
//		select {
//		case res := <-fan:
//			fmt.Println("received result")
//			if res.err != nil {
//				t.Error(res.err)
//			}
//			fmt.Printf("first number is %d", res.out)
//		case <-time.After(time.Second * 10):
//			t.Error("timeout")
//			return
//		}
//	}
//}

func TestSimpleWorkerPool(t *testing.T) {
	s := make(chan []job[int])
	r := make(chan response[int])

	ctx := context.Background()
	wp := &SimpleWorkerPool[int]{
		workers: []worker[int]{
			&testWorker[int]{
				ctx: ctx,
			},
			&testWorker[int]{
				ctx: ctx,
			},
			&testWorker[int]{
				ctx: ctx,
			},
		},
		jobs:    s,
		results: r,
	}

	go wp.Start()
	fmt.Println("Starting worker pool from test")

	jobs := make([]job[int], 10)
	for i := 0; i < 10; i++ {
		fmt.Println(32312)
		jobs[i] = &testJob[int]{x: i, y: rand.Intn(10)}
	}

	go func() {
		wp.jobs <- jobs
	}()
	time.Sleep(time.Second * 10)
}

func TestLearn(t *testing.T) {
	server := http.NewServeMux()
	m := &loggerMiddleware{}
	server.Handle("/", m.Logger(http.HandlerFunc(simpleHandler)))
	server.Handle("/learn", m.Logger(http.HandlerFunc(simpleHandler)))

	fmt.Println("Server starting on :8080...")
	err := http.ListenAndServe(":8080", server)
	if err != nil {
		fmt.Printf("Server start error: %s\n", err)
		return
	}
	fmt.Println("Server stopped.")
}

func TestGoroutineLearn(t *testing.T) {
	//testMemory()
	var mu sync.Mutex
	badIdea(&mu)
}

func warmServiceConnCache() *sync.Pool {
	p := &sync.Pool{
		New: connectToService,
	}
	for i := 0; i < 10; i++ {
		p.Put(p.New())
	}
	return p
}
func startNetworkDaemon() *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		//connPool := warmServiceConnCache()
		server, err := net.Listen("tcp", "localhost:8080")
		if err != nil {
			log.Fatalf("cannot listen: %v", err)
		}
		defer server.Close()
		wg.Done()
		for {
			conn, err := server.Accept()
			if err != nil {
				log.Printf("cannot accept connection: %v", err)
				continue
			}
			connectToService()
			fmt.Fprintln(conn, "")
			conn.Close()
		}
	}()
	return &wg
}

func init() {
	daemonStarted := startNetworkDaemon()
	daemonStarted.Wait()
}

func connectToService() interface{} {
	time.Sleep(1 * time.Second)
	return struct{}{}
}

func BenchmarkNetworkRequest(b *testing.B) {
	for i := 0; i < b.N; i++ {
		conn, err := net.Dial("tcp", "localhost:8080")
		if err != nil {
			b.Fatalf("cannot dial host: %v", err)
		}
		if _, err := ioutil.ReadAll(conn); err != nil {
			b.Fatalf("cannot read: %v", err)
		}
		conn.Close()
	}
}

func BenchmarkContextSwitch(b *testing.B) {
	var wg sync.WaitGroup
	begin := make(chan struct{})
	c := make(chan struct{})
	var token struct{}
	sender := func() {
		defer wg.Done()
		// wait until signaled
		<-begin
		for i := 0; i < b.N; i++ {
			// empty struct no operational heavy we only test communication time
			c <- token
		}
	}
	receiver := func() {
		defer wg.Done()
		// wait until signaled
		<-begin
		for i := 0; i < b.N; i++ {
			<-c
		}
	}
	wg.Add(2)
	go sender()
	go receiver()
	b.StartTimer()
	// signal begin
	close(begin)
	wg.Wait()
}

func TestMutex(t *testing.T) {
	producer := func(wg *sync.WaitGroup, l sync.Locker) {
		defer wg.Done()
		for i := 5; i > 0; i-- {
			l.Lock()
			l.Unlock()
			time.Sleep(1)
		}
	}
	observer := func(wg *sync.WaitGroup, l sync.Locker) {
		defer wg.Done()
		l.Lock()
		defer l.Unlock()
	}
	test := func(count int, mutex, rwMutex sync.Locker) time.Duration {
		var wg sync.WaitGroup
		wg.Add(count + 1)
		beginTestTime := time.Now()
		go producer(&wg, mutex)
		for i := count; i > 0; i-- {
			go observer(&wg, rwMutex)
		}
		wg.Wait()
		return time.Since(beginTestTime)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 1, 2, ' ', 0)
	defer tw.Flush()
	var m sync.RWMutex
	fmt.Fprintf(tw, "Readers\tRWMutext\tMutex\n")
	for i := 0; i < 2; i++ {
		count := int(math.Pow(2, float64(i)))
		fmt.Fprintf(
			tw,
			"%d\t%v\t%v\n",
			count,
			test(count, &m, m.RLocker()),
			test(count, &m, &m),
		)
	}
}

func badIdea(mu *sync.Mutex) {
	mu.Lock()
	fmt.Println("First lock acquired")
	// Some code...
	mu.Lock() // THIS WILL DEADLOCK - same goroutine tries to lock again
	fmt.Println("Second lock acquired (this won't print)")
	mu.Unlock()
	mu.Unlock()
}

func testMemory() {
	memConsumed := func() uint64 {
		runtime.GC()
		var s runtime.MemStats
		runtime.ReadMemStats(&s)
		return s.Sys
	}
	var c <-chan interface{}
	var wg sync.WaitGroup
	noop := func() { wg.Done(); <-c }
	const numGoroutines = 12.8e6
	wg.Add(numGoroutines)
	before := memConsumed()
	for i := numGoroutines; i > 0; i-- {
		go noop()
	}
	wg.Wait()
	after := memConsumed()
	fmt.Printf("%.3fMB", float64(after-before)/1000/1000)
}

type middleware interface {
	Logger(handler http.Handler) http.Handler
}

type loggerMiddleware struct{}

type wrappedResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *wrappedResponseWriter) WriteHeader(statusCode int) {
	w.ResponseWriter.WriteHeader(statusCode)
	w.statusCode = statusCode
}

func (l *loggerMiddleware) Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &wrappedResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)
		duration := time.Since(start)
		authHeader := r.Header.Get("Authorization")
		fmt.Printf("%s %s - Authorization: %s - Status: %d - Duration: %v\n", r.Method, r.URL.Path, authHeader, wrapped.statusCode, duration)
	})
}

func simpleHandler(w http.ResponseWriter, r *http.Request) {
	sso := r.Header.Get("Authorization")
	if sso == "" {
		http.Error(w, fmt.Sprintf("no Authorization Header set: %s\n", "unauthenticated request"), http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func Hi(chn chan<- string) {
	chn <- "Hi"
}
