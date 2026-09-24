//go:build externaljobs

package api

import "errors"

func driverTarget(category, kind string) any {
	switch category + "/" + kind {
	case "check/http":
		return &PulseHTTPConfig{}
	case "check/tcp":
		return &PulseTCPConfig{}
	case "check/icmp":
		return &PulseICMPConfig{}
	case "check/dns":
		return &PulseDNSConfig{}
	case "check/udp":
		return &PulseUDPConfig{}
	case "check/grpc":
		return &PulseGRPCConfig{}
	case "check/docker":
		return &PulseDockerConfig{}
	case "check/redis":
		return &PulseRedisConfig{}
	case "check/postgres":
		return &PulsePostgresConfig{}
	case "check/mysql":
		return &PulseMySQLConfig{}
	case "check/mongo":
		return &PulseMongoConfig{}
	case "check/rabbitmq":
		return &PulseRabbitMQConfig{}
	case "check/kafka":
		return &PulseKafkaConfig{}
	case "check/tls":
		return &PulseTLSConfig{}
	case "recovery/kubernetes":
		return &InterventionTargetKubernetes{}
	case "recovery/webhook":
		return &InterventionTargetWebhook{}
	case "recovery/systemd":
		return &InterventionTargetSystemd{}
	case "recovery/aws":
		return &InterventionTargetAWS{}
	case "recovery/docker":
		return &InterventionTargetDocker{}
	case "notification/email":
		return &CodeNotificationEmail{}
	case "notification/webhook":
		return &CodeNotificationWebhook{}
	case "notification/log":
		return &CodeNotificationLog{}
	case "notification/pagerduty":
		return &CodeNotificationPagerDuty{}
	case "notification/slack":
		return &CodeNotificationSlack{}
	case "notification/telegram":
		return &CodeNotificationTelegram{}
	case "notification/discord":
		return &CodeNotificationDiscord{}
	case "notification/opsgenie":
		return &CodeNotificationOpsgenie{}
	case "notification/teams":
		return &CodeNotificationTeams{}
	case "notification/mattermost":
		return &CodeNotificationMattermost{}
	case "notification/pushover":
		return &CodeNotificationPushover{}
	case "notification/twilio":
		return &CodeNotificationTwilio{}
	case "notification/datadog":
		return &CodeNotificationDatadog{}
	case "notification/victorops":
		return &CodeNotificationVictorOps{}
	case "check/external", "recovery/external", "notification/external":
		return &ExternalConfig{}
	}
	return nil
}
func validateAdditionalResource(r Resource) error {
	if r.Kind == "JobType" {
		var spec JobTypeSpec
		if err := StrictDecode(r.Spec, &spec); err != nil {
			return err
		}
		if spec.Version == "" || spec.Handler == "" || spec.ProtocolVersion == "" {
			return errors.New("JobType version, handler and protocol are required")
		}
		switch spec.Kind {
		case "check", "recovery", "notification":
		default:
			return errors.New("invalid JobType kind")
		}
		return nil
	}
	return errors.New("unsupported resource kind")
}
