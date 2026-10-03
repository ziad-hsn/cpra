//go:build !externaljobs

package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type serverConfigExtensions struct{}
type serverExtensions struct{}
type managementExtensions struct{}
type snapshotExtensions struct{}

func (*Server) validateExtensions() error               { return nil }
func (ServerConfig) defaultWriteTimeout() time.Duration { return 30 * time.Second }
func (*Server) stopExtensionAdmission()                 {}
func (*Server) stopExtensions(context.Context) error    { return nil }

func (*Server) registerManagementExtensions(*http.ServeMux, *managementHTTP) {}
func (*managementHTTP) discoverExtensions(*api.Capabilities)                 {}
func (*managementHTTP) extensionPermissions(map[string]bool)                 {}
func (*managementHTTP) reserveSnapshotExtension(string, string) (snapshotExtensions, error) {
	return snapshotExtensions{}, nil
}
func (snapshotExtensions) retain()  {}
func (snapshotExtensions) release() {}
