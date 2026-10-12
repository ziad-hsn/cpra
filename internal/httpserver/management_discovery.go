package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/version"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func (s *Server) registerAccessDiscovery(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v2/access", func(w http.ResponseWriter, r *http.Request) {
		managementHeaders(w)
		if r.URL.RawQuery != "" {
			writeManagementError(w, managementFailure(400, "invalidQuery", "Access discovery does not accept query parameters."))
			return
		}
		mode := api.AccessDiscoveryModeObservation
		if s.cfg.Management != nil && s.cfg.ManagementAuth != nil {
			mode = api.AccessDiscoveryModeManagement
		}
		writeManagementJSON(w, http.StatusOK, api.AccessDiscovery{Mode: mode, AuthenticationRequired: !s.anonymousLoopbackConfigured()})
	})
}

func (m *managementHTTP) registerDiscovery(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v2/version", func(w http.ResponseWriter, r *http.Request) {
		managementHeaders(w)
		if _, err := m.auth.Authorize(r, "GetVersion"); err != nil {
			writeManagementError(w, err)
			return
		}
		identity := version.Current()
		result := api.Version{Version: identity.Version, GoVersion: runtime.Version(), StorageFormat: int64(persistence.LatestFormatVersion)}
		if identity.Commit != "unknown" {
			result.Commit = identity.Commit
		}
		if at, err := time.Parse(time.RFC3339, identity.SourceDate); err == nil {
			result.SourceTime = &at
		}
		writeManagementJSON(w, http.StatusOK, result)
	})
	// The canonical embedded contract is parsed once during route registration.
	// Selected roots and their referenced definitions are served without I/O or
	// a controller/FSM lock. No provider configuration values enter this document.
	var contract struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	schemaErr := json.Unmarshal(api.Schema(), &contract)
	resources := map[string]bool{"Incident": true, "Action": true, "Operation": true, "Event": true}
	for _, route := range managementResourceRoutes {
		resources[route.kind] = true
	}
	extensions := api.Capabilities{ResourceOperations: map[string][]string{}}
	m.discoverExtensions(&extensions)
	for _, name := range extensions.Resources {
		if _, exists := contract.Components.Schemas[name]; exists {
			resources[name] = true
		}
	}
	mux.HandleFunc("GET /api/v2/schemas/{id}", func(w http.ResponseWriter, r *http.Request) {
		managementHeaders(w)
		if _, err := m.auth.Authorize(r, "ExplainResource"); err != nil {
			writeManagementError(w, err)
			return
		}
		if schemaErr != nil {
			writeManagementError(w, managementFailure(503, "schemaUnavailable", "The build's resource contract is unavailable."))
			return
		}
		id := r.PathValue("id")
		var root string
		for candidate := range resources {
			if strings.EqualFold(candidate, id) {
				root = candidate
				break
			}
		}
		if root == "" || r.URL.RawQuery != "" {
			writeManagementError(w, managementFailure(404, "schemaNotFound", "Select a registered resource kind from API discovery."))
			return
		}
		schema, description, err := resourceExplanation(root, contract.Components.Schemas)
		if err != nil {
			writeManagementError(w, managementFailure(503, "schemaUnavailable", "The resource contract is unavailable."))
			return
		}
		writeManagementJSON(w, http.StatusOK, api.Explanation{Resource: root, Description: description, Schema: schema})
	})
}

func resourceExplanation(root string, schemas map[string]json.RawMessage) (json.RawMessage, string, error) {
	definitions := map[string]json.RawMessage{}
	var description string
	var visit func(string) error
	visit = func(name string) error {
		if _, exists := definitions[name]; exists {
			return nil
		}
		raw, exists := schemas[name]
		if !exists {
			return errors.New("canonical resource schema missing")
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		definitions[name] = nil // Break recursive model references.
		var rewrite func(any) error
		rewrite = func(node any) error {
			switch node := node.(type) {
			case map[string]any:
				if ref, ok := node["$ref"].(string); ok {
					const prefix = "#/components/schemas/"
					if strings.HasPrefix(ref, prefix) {
						next := strings.TrimPrefix(ref, prefix)
						if err := visit(next); err != nil {
							return err
						}
						node["$ref"] = "#/$defs/" + next
					}
				}
				for _, child := range node {
					if err := rewrite(child); err != nil {
						return err
					}
				}
			case []any:
				for _, child := range node {
					if err := rewrite(child); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if name == root {
			if object, ok := value.(map[string]any); ok {
				description, _ = object["description"].(string)
			}
		}
		if err := rewrite(value); err != nil {
			return err
		}
		definitions[name], _ = json.Marshal(value)
		return nil
	}
	if err := visit(root); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "$ref": "#/$defs/" + root, "$defs": definitions})
	return raw, description, err
}
