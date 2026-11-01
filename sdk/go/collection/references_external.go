//go:build externaljobs

package collection

import (
	"encoding/json"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func additionalReferences(item Item, add func(string, string) error) error {
	var drivers []api.DriverConfig
	switch item.Resource.Kind {
	case "Monitor":
		var spec api.MonitorSpec
		if err := json.Unmarshal(item.Resource.Spec, &spec); err != nil {
			return err
		}
		drivers = append(drivers, spec.Check.Driver)
		if spec.Recovery != nil {
			if err := api.VisitRecoveryDrivers(spec.Recovery, func(_ string, driver *api.DriverConfig) error { drivers = append(drivers, *driver); return nil }); err != nil {
				return err
			}
		}
		if spec.Notifications != nil {
			for _, rule := range *spec.Notifications {
				if rule.Driver != nil {
					drivers = append(drivers, *rule.Driver)
				}
			}
		}
	case "NotificationEndpoint":
		var driver api.DriverConfig
		if err := json.Unmarshal(item.Resource.Spec, &driver); err != nil {
			return err
		}
		drivers = append(drivers, driver)
	}
	for _, driver := range drivers {
		if driver.Type == "external" {
			var config api.ExternalConfig
			if err := json.Unmarshal(driver.Config, &config); err != nil {
				return err
			}
			if err := add("JobType", config.JobTypeID); err != nil {
				return err
			}
		}
	}
	return nil
}
