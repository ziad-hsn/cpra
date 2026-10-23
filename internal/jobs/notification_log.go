package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

var errNotificationLog = errors.New("notification log requires a regular private file inside notifications.log_directory")
var errNotificationLogDirectoryAccess = errors.New("notifications.log_directory requires service-account ownership and private directory access; on Unix use owner-only permissions such as 0700, and on Windows use the protected service DACL")

// NotificationLog owns the process-configured output root. Monitor inputs can
// select relative files inside it, but cannot select another filesystem root.
// The process closes it only after all notification jobs have joined.
type NotificationLog struct{ root *os.Root }

// ValidateNotificationLogLocation checks configured path relationships without
// opening or creating files. Runtime opening also verifies native identities,
// permissions and aliases, which a lexical validation cannot establish.
func ValidateNotificationLogLocation(directory string, protected ...string) error {
	if directory == "" {
		return nil
	}
	if !filepath.IsAbs(directory) {
		return errors.New("notifications.log_directory must be absolute")
	}
	for _, path := range protected {
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return errors.New("protected notification-log boundary is invalid")
		}
		if logPathContains(directory, absolute) || logPathContains(absolute, directory) {
			return errors.New("notifications.log_directory must be separate from state, manifest, runtime and authentication directories")
		}
	}
	return nil
}

func OpenNotificationLog(directory string, protected ...string) (*NotificationLog, error) {
	if err := ValidateNotificationLogLocation(directory, protected...); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(directory) {
		return nil, errors.New("notifications.log_directory must be absolute")
	}
	var protectedDirectories []os.FileInfo
	var protectedPaths []string
	for _, p := range protected {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, errNotificationLog
		}
		abs, err = resolveLogProtectedPath(abs)
		if err != nil {
			return nil, errNotificationLog
		}
		protectedPaths = append(protectedPaths, abs)
		if logPathContains(directory, abs) || logPathContains(abs, directory) {
			return nil, errors.New("notification log directory must be separate from state and protected configuration")
		}
		if file, err := os.Open(abs); err == nil {
			info, statErr := file.Stat()
			file.Close()
			if statErr != nil || !info.IsDir() {
				return nil, errNotificationLog
			}
			protectedDirectories = append(protectedDirectories, info)
		} else if !os.IsNotExist(err) {
			return nil, errNotificationLog
		}
	}
	root, ancestors, err := acquireNotificationLogRoot(filepath.Clean(directory), protectedDirectories...)
	if err != nil {
		if errors.Is(err, errNotificationLogDirectoryAccess) {
			return nil, errNotificationLogDirectoryAccess
		}
		if errors.Is(err, os.ErrPermission) {
			return nil, errors.New("notifications.log_directory cannot be read or created by the service account; provision its ownership and private access before startup")
		}
		return nil, errNotificationLog
	}
	if err := checkProtectedLogIdentities(ancestors, protectedPaths); err != nil {
		root.Close()
		return nil, errNotificationLog
	}
	return &NotificationLog{root: root}, nil
}

func resolveLogProtectedPath(path string) (string, error) {
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", errNotificationLog
		}
		// ENOENT may describe an existing dangling alias rather than an absent
		// name. Its lexical parents do not establish protected containment.
		if _, entryErr := os.Lstat(path); !os.IsNotExist(entryErr) {
			return "", errNotificationLog
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", errNotificationLog
		}
		missing = append(missing, filepath.Base(path))
		path = parent
	}
}

func logPathContains(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (l *NotificationLog) open(name string) (*os.File, error) {
	if l == nil || l.root == nil || api.ValidateNotificationLogPath(name) != nil {
		return nil, errNotificationLog
	}
	local, err := filepath.Localize(name)
	if err != nil {
		return nil, errNotificationLog
	}
	parent := l.root
	for _, name := range strings.Split(filepath.Dir(local), string(filepath.Separator)) {
		if name == "." {
			continue
		}
		next, _, err := acquireNotificationLogChild(parent, name, true, true)
		if parent != l.root {
			parent.Close()
		}
		if err != nil {
			return nil, errNotificationLog
		}
		parent = next
	}
	if parent != l.root {
		defer parent.Close()
	}
	f, err := openNotificationLogFile(parent, filepath.Base(local))
	if err != nil {
		return nil, errNotificationLog
	}
	if err := checkNotificationLogFile(f); err != nil {
		_ = f.Close()
		return nil, errNotificationLog
	}
	return f, nil
}

func (l *NotificationLog) Close() error {
	if l == nil || l.root == nil {
		return nil
	}
	return l.root.Close()
}

// Acquire one component at a time. Each parent is held open and verified before
// creating/opening its child, and a non-following child handle supplies identity.
func acquireNotificationLogRoot(directory string, protected ...os.FileInfo) (*os.Root, []os.FileInfo, error) {
	base := filepath.VolumeName(directory) + string(filepath.Separator)
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, nil, err
	}
	parent, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	err = checkNotificationLogDirectory(parent, false)
	first, statErr := parent.Stat()
	_ = parent.Close()
	if err != nil || statErr != nil {
		root.Close()
		return nil, nil, errNotificationLog
	}
	ancestors := []os.FileInfo{first}
	conflicts := func(info os.FileInfo) bool {
		for _, boundary := range protected {
			if os.SameFile(info, boundary) {
				return true
			}
		}
		return false
	}
	if conflicts(first) {
		root.Close()
		return nil, nil, errNotificationLog
	}
	relative, err := filepath.Rel(base, directory)
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	components := strings.Split(relative, string(filepath.Separator))
	for i, name := range components {
		next, info, err := acquireNotificationLogChild(root, name, i == len(components)-1, false)
		_ = root.Close()
		if err != nil {
			return nil, nil, err
		}
		root = next
		// Reject an alias of a protected directory before descending into it or
		// creating any of its children, not only before the first log append.
		if conflicts(info) {
			root.Close()
			return nil, nil, errNotificationLog
		}
		ancestors = append(ancestors, info)
	}
	return root, ancestors, nil
}

func acquireNotificationLogChild(parent *os.Root, name string, private, concurrentCreation bool) (*os.Root, os.FileInfo, error) {
	held, err := openNotificationLogDirectory(parent, name)
	if os.IsNotExist(err) {
		// Initial root creation never adopts another actor's entry. Inside the
		// private acquired root, concurrent notifications may create the same
		// folder; reopen it through all no-follow, ownership and identity checks.
		if err = parent.Mkdir(name, 0700); err == nil || concurrentCreation && os.IsExist(err) {
			held, err = openNotificationLogDirectory(parent, name)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	defer held.Close()
	if err := checkNotificationLogDirectory(held, private); err != nil {
		return nil, nil, err
	}
	expected, err := held.Stat()
	if err != nil {
		return nil, nil, err
	}
	next, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	actualFile, err := next.Open(".")
	var actual os.FileInfo
	if err == nil {
		actual, err = actualFile.Stat()
		_ = actualFile.Close()
	}
	if err != nil || !os.SameFile(expected, actual) {
		next.Close()
		return nil, nil, errNotificationLog
	}
	return next, actual, nil
}

// Native identities also detect case-insensitive and short-name aliases. Text
// comparisons alone cannot establish that state and log directories differ.
func checkProtectedLogIdentities(logAncestors []os.FileInfo, protected []string) error {
	logRoot := logAncestors[len(logAncestors)-1]
	for _, path := range protected {
		if path == "" {
			continue
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return errNotificationLog
		}
		leaf := true
		for {
			file, err := os.Open(path)
			if err == nil {
				info, statErr := file.Stat()
				file.Close()
				if statErr != nil {
					return errNotificationLog
				}
				if info.IsDir() {
					if os.SameFile(info, logRoot) {
						return errNotificationLog
					}
					if leaf {
						for _, ancestor := range logAncestors {
							if os.SameFile(info, ancestor) {
								return errNotificationLog
							}
						}
					}
				}
			} else if !os.IsNotExist(err) {
				return errNotificationLog
			}
			parent := filepath.Dir(path)
			if parent == path {
				break
			}
			path, leaf = parent, false
		}
	}
	return nil
}
