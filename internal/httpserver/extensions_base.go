//go:build !externaljobs

package httpserver

import (
	"net/http"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type serverConfigExtensions struct{}
type managementExtensions struct{}
type snapshotExtensions struct{}

func (*Server) validateExtensions() error { return nil }

func (*Server) registerManagementExtensions(*http.ServeMux, *managementHTTP) {}
func (*managementHTTP) discoverExtensions(*api.Capabilities)                 {}
func (*managementHTTP) extensionPermissions(map[string]bool)                 {}
func (*managementHTTP) reserveSnapshotExtension(string, string) (snapshotExtensions, error) {
	return snapshotExtensions{}, nil
}
func (snapshotExtensions) retain()  {}
func (snapshotExtensions) release() {}
