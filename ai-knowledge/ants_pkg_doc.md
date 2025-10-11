package ants // import "github.com/panjf2000/ants/v2"

Package ants implements an efficient and reliable goroutine pool for Go.

With ants, Go applications are able to limit the number of active goroutines,
recycle goroutines efficiently, and reduce the memory footprint significantly.
Package ants is extremely useful in the scenarios where a massive number of
goroutines are created and destroyed frequently, such as highly-concurrent batch
processing systems, HTTP servers, services of asynchronous tasks, etc.

const DefaultAntsPoolSize = math.MaxInt32 ...
const OPENED = iota ...
var ErrLackPoolFunc = errors.New("must provide function for pool") ...
func Cap() int
func Free() int
func Reboot()
func Release()
func ReleaseTimeout(timeout time.Duration) error
func Running() int
func Submit(task func()) error
type LoadBalancingStrategy int
    const RoundRobin LoadBalancingStrategy = 1 << (iota + 1) ...
type Logger interface{ ... }
type MultiPool struct{ ... }
    func NewMultiPool(size, sizePerPool int, lbs LoadBalancingStrategy, options ...Option) (*MultiPool, error)
type MultiPoolWithFunc struct{ ... }
    func NewMultiPoolWithFunc(size, sizePerPool int, fn func(any), lbs LoadBalancingStrategy, ...) (*MultiPoolWithFunc, error)
type MultiPoolWithFuncGeneric[T any] struct{ ... }
    func NewMultiPoolWithFuncGeneric[T any](size, sizePerPool int, fn func(T), lbs LoadBalancingStrategy, ...) (*MultiPoolWithFuncGeneric[T], error)
type Option func(opts *Options)
    func WithDisablePurge(disable bool) Option
    func WithExpiryDuration(expiryDuration time.Duration) Option
    func WithLogger(logger Logger) Option
    func WithMaxBlockingTasks(maxBlockingTasks int) Option
    func WithNonblocking(nonblocking bool) Option
    func WithOptions(options Options) Option
    func WithPanicHandler(panicHandler func(any)) Option
    func WithPreAlloc(preAlloc bool) Option
type Options struct{ ... }
type Pool struct{ ... }
    func NewPool(size int, options ...Option) (*Pool, error)
type PoolWithFunc struct{ ... }
    func NewPoolWithFunc(size int, pf func(any), options ...Option) (*PoolWithFunc, error)
type PoolWithFuncGeneric[T any] struct{ ... }
    func NewPoolWithFuncGeneric[T any](size int, pf func(T), options ...Option) (*PoolWithFuncGeneric[T], error)
