package management

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// resolveDriverWithLookup resolves inert configuration, without provider I/O.
// Temporary reservations are released before return. On success, releaseOutput
// covers the encoded driver.Config until its caller's last use; callers must not
// release it while validation or another consumer still uses that output. A nil
// reservation hook preserves the existing map-based validation behavior.
// Lookup implementations separately account/release the borrowed resource.
// The input driver remains unchanged on all errors, including cancellation.
func resolveDriverWithLookup(ctx context.Context, driver *api.DriverConfig, category string, lookup resourceLookup, reserve reserveResourceScratch) (releaseOutput func(), err error) {
	if ctx == nil || driver == nil {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateDriverCredentialScope(*driver); err != nil {
		return nil, err
	}
	var scratch []func()
	defer func() {
		for i := len(scratch) - 1; i >= 0; i-- {
			scratch[i]()
		}
	}()
	charge := func(size int) error {
		if reserve == nil {
			return ctx.Err()
		}
		release, err := reserveDriverScratch(ctx, reserve, size)
		if err != nil {
			return err
		}
		scratch = append(scratch, release)
		return nil
	}
	if err := chargeDriverBytes(ctx, charge, 2, len(driver.Config)); err != nil {
		return nil, err
	}
	var config map[string]json.RawMessage
	defer func() {
		for _, value := range config {
			clear(value)
		}
	}()
	if api.StrictDecode(driver.Config, &config) != nil || config == nil {
		return nil, ErrValidation
	}
	if driver.CredentialRefs != nil {
		if lookup == nil && len(*driver.CredentialRefs) != 0 {
			return nil, ErrUnavailable
		}
		// A stable order makes cancellation, accounting and first-error behavior
		// deterministic; the resulting JSON object has no field-order semantics.
		if err := chargeDriverBytes(ctx, charge, 16, len(*driver.CredentialRefs)); err != nil {
			return nil, err
		}
		for _, field := range slices.Sorted(maps.Keys(*driver.CredentialRefs)) {
			id := (*driver.CredentialRefs)[field]
			key := persistence.CatalogKey{Kind: "Credential", ID: id}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			found, err := lookup.withResource(ctx, key, func(resource *api.Resource) error {
				if resource == nil || resource.Kind != key.Kind || resource.Metadata.ID != key.ID {
					return ErrUnavailable
				}
				if err := chargeDriverBytes(ctx, charge, 2, len(resource.Spec)); err != nil {
					return err
				}
				var credential api.CredentialSpec
				if api.StrictDecode(resource.Spec, &credential) != nil || credential.Value == nil {
					return ErrUnavailable
				}
				// Strings remain literal, including passwords which look like JSON.
				// At most six bytes per input byte plus quotes are needed by JSON
				// escaping. Reserve before creating that retained value.
				if err := chargeDriverBytes(ctx, charge, 6, len(*credential.Value), 1); err != nil {
					return err
				}
				value, _ := json.Marshal(*credential.Value)
				keep := false
				defer func() {
					if !keep {
						clear(value)
					}
				}()
				valid, err := validateDriverCredential(ctx, charge, category, driver.Type, field, value)
				if err != nil {
					return err
				}
				if !valid {
					clear(value)
					// Structured slots use JSON text and the same typed schema.
					if err := chargeDriverBytes(ctx, charge, 1, len(*credential.Value)); err != nil {
						return err
					}
					value = json.RawMessage(*credential.Value)
					valid, err = validateDriverCredential(ctx, charge, category, driver.Type, field, value)
					if err != nil {
						return err
					}
					if !valid {
						return errors.Join(ErrValidation, errors.New("credential value does not match its driver field type"))
					}
				}
				clear(config[field])
				config[field] = value // An owned encoding, never borrowed resource data.
				keep = true
				return nil
			})
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, persistence.ErrCatalogDependency
			}
		}
	}
	// Reserve the output separately from validation scratch: it deliberately
	// outlives this call. HTML escaping can expand raw values and object keys.
	outputBytes := 2
	for field, value := range config {
		if err := chargeDriverBytes(ctx, func(n int) error {
			maximum := int(^uint(0) >> 1)
			if n > maximum-outputBytes {
				return ErrGraphLimit
			}
			outputBytes += n
			return nil
		}, 6, len(field), len(value), 4); err != nil {
			return nil, err
		}
	}
	if err := chargeDriverBytes(ctx, charge, 1, outputBytes); err != nil {
		return nil, err
	}
	release, err := reserveDriverScratch(ctx, reserve, outputBytes)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			release()
		}
	}()
	resolved, err := json.Marshal(config)
	if err != nil {
		return nil, ErrValidation
	}
	next := api.DriverConfig{Type: driver.Type, Config: resolved}
	if err := ctx.Err(); err != nil {
		clear(resolved)
		return nil, err
	}
	if api.ValidateDriver(category, next) != nil {
		clear(resolved)
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		clear(resolved)
		return nil, err
	}
	*driver = next
	retained = true
	return release, nil
}

func validateDriverCredential(ctx context.Context, charge func(int) error, category, kind, field string, value json.RawMessage) (bool, error) {
	// Account for the marshaled probe and its typed validation copy. The factor
	// includes the worst-case six-byte JSON escaping without pre-encoding a copy.
	if err := chargeDriverBytes(ctx, charge, 12, len(field), len(value), 4); err != nil {
		return false, err
	}
	probe, err := json.Marshal(map[string]json.RawMessage{field: value})
	if err != nil {
		return false, nil
	}
	defer clear(probe)
	valid := api.ValidateDriver(category, api.DriverConfig{Type: kind, Config: probe}) == nil
	return valid, ctx.Err()
}

func chargeDriverBytes(ctx context.Context, charge func(int) error, multiplier int, sizes ...int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	maximum := int(^uint(0) >> 1)
	if multiplier < 1 {
		return ErrGraphLimit
	}
	total := 0
	for _, size := range sizes {
		if size < 0 || size > maximum-total {
			return ErrGraphLimit
		}
		total += size
	}
	if total > maximum/multiplier {
		return ErrGraphLimit
	}
	if charge != nil {
		if err := charge(total * multiplier); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// reserveDriverScratch normalizes optional hooks and releases a successful
// reservation if cancellation arrived during the accounting callback.
func reserveDriverScratch(ctx context.Context, reserve reserveResourceScratch, size int) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if reserve == nil {
		return func() {}, nil
	}
	release, err := reserve(size)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
