package collection

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// Source identifies a file, directory, URL, or caller-owned reader. Constructors
// keep source fetching separate from the authenticated CPRa client.
type Source struct {
	path   string
	url    string
	name   string
	reader io.Reader
}

// File selects a file or directory for Freeze. Recursive traversal requires
// Options.Recursive; this constructor does not read the filesystem.
func File(path string) Source { return Source{path: path} }

// URL selects an explicit remote input for Freeze's separate source client.
// CPRa authentication is never inherited by that client.
func URL(address string) Source { return Source{url: address} }

// Reader consumes r once; ownership and closing remain with its caller.
func Reader(name string, r io.Reader) Source { return Source{name: name, reader: r} }

// Options bounds plaintext client staging. A zero field selects its default.
// SourceTransport, when supplied, must be an unauthenticated source transport;
// it must not inject API or provider credentials.
type Options struct {
	Recursive       bool
	AllowHTTP       bool
	TempDir         string
	MaxStagingBytes int64
	// MaxSourceBytes bounds cumulative raw inputs, separately from peak staging.
	// Zero defaults to MaxStagingBytes. Typed streams count serialized objects plus LF.
	MaxSourceBytes   int64
	MaxResourceBytes int
	MaxDocumentBytes int
	MaxResources     int
	SourceTimeout    time.Duration
	SourceTransport  http.RoundTripper
}

func (o Options) normalized() (Options, error) {
	if o.MaxStagingBytes == 0 {
		o.MaxStagingBytes = 1 << 30
	}
	if o.MaxSourceBytes == 0 {
		o.MaxSourceBytes = o.MaxStagingBytes
	}
	if o.MaxResourceBytes == 0 {
		o.MaxResourceBytes = 1 << 20
	}
	if o.MaxDocumentBytes == 0 {
		o.MaxDocumentBytes = 16 << 20
	}
	if o.MaxResources == 0 {
		o.MaxResources = 2_000_000
	}
	if o.SourceTimeout == 0 {
		o.SourceTimeout = 30 * time.Second
	}
	if o.MaxStagingBytes < 1 || o.MaxStagingBytes == math.MaxInt64 || o.MaxSourceBytes < 1 || uint64(o.MaxSourceBytes) > commitment.MaxSourceBytes || o.MaxResourceBytes < 1 || o.MaxResourceBytes > 1<<20 || o.MaxDocumentBytes < o.MaxResourceBytes || o.MaxResources < 1 || uint64(o.MaxResources) > commitment.MaxItems || o.SourceTimeout < 0 {
		return o, fmt.Errorf("invalid collection limits")
	}
	return o, nil
}

func expand(ctx context.Context, sources []Source, recursive bool) ([]Source, error) {
	return expandLimit(ctx, sources, recursive, commitment.MaxSources)
}

func expandLimit(ctx context.Context, sources []Source, recursive bool, maxSources uint64) ([]Source, error) {
	var out []Source
	seen := make(map[string]bool)
	add := func(path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		canonical, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return err
		}
		info, err := os.Stat(canonical)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source must be a regular file: %s", path)
		}
		if !seen[canonical] {
			if uint64(len(out)) >= maxSources {
				return fmt.Errorf("source count exceeds limit")
			}
			seen[canonical] = true
			out = append(out, File(canonical))
		}
		return nil
	}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if source.path == "" {
			if uint64(len(out)) >= maxSources {
				return nil, fmt.Errorf("source count exceeds limit")
			}
			if source.url == "" && (source.reader == nil || source.name == "") {
				return nil, fmt.Errorf("reader source requires a name and reader")
			}
			out = append(out, source)
			continue
		}
		info, err := os.Lstat(source.path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Stat(source.path)
			if err != nil {
				return nil, err
			}
			if target.IsDir() {
				return nil, fmt.Errorf("directory symlink is not traversed: %s", source.path)
			}
		}
		if !info.IsDir() {
			if err := add(source.path); err != nil {
				return nil, err
			}
			continue
		}
		err = filepath.WalkDir(source.path, func(path string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path != source.path && !recursive {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".yaml", ".yml", ".json":
				return add(path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s Source) open(ctx context.Context, o Options) (io.ReadCloser, string, error) {
	if s.path != "" {
		f, err := os.Open(s.path)
		return f, s.path, err
	}
	if s.url == "" {
		return io.NopCloser(s.reader), s.name, nil
	}
	u, err := url.Parse(s.url)
	if err != nil {
		return nil, "URL source", fmt.Errorf("invalid source URL")
	}
	label := safeURL(u)
	if err := allowURL(u, o.AllowHTTP); err != nil {
		return nil, label, err
	}
	transport := o.SourceTransport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := &http.Client{Transport: transport, Timeout: o.SourceTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("source redirect limit exceeded")
		}
		if err := allowURL(req.URL, o.AllowHTTP); err != nil {
			return err
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("source HTTPS downgrade rejected")
		}
		req.Header = make(http.Header)
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, label, fmt.Errorf("invalid source request")
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, label, ctx.Err()
		}
		// net/http errors can contain signed query strings; never wrap them.
		return nil, label, fmt.Errorf("source request failed")
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, label, fmt.Errorf("source returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > o.MaxStagingBytes {
		_ = response.Body.Close()
		return nil, label, fmt.Errorf("source exceeds staging quota")
	}
	return response.Body, label, nil
}

func allowURL(u *url.URL, allowHTTP bool) error {
	if u.User != nil {
		return fmt.Errorf("URL user credentials are not accepted")
	}
	if u.Host == "" || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) {
		return fmt.Errorf("source URL requires HTTPS; HTTP requires explicit opt-in")
	}
	if u.Fragment != "" {
		return fmt.Errorf("source URL fragments are not accepted")
	}
	return nil
}

func safeURL(u *url.URL) string {
	copy := *u
	copy.User = nil
	copy.RawQuery = ""
	copy.Fragment = ""
	return copy.String()
}
