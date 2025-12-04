// Package alerts provides alerting and policy management for CPRA monitors.
//
// The alerts package defines interfaces and implementations for managing
// alert policies and dispatching notifications when monitor thresholds are exceeded.
// This package is currently in development and will provide:
//
//   - Alert policy definitions and evaluation
//   - Alert routing and deduplication
//   - Integration with external alerting systems
//   - Alert history and state management
//
// # Future Architecture
//
// The alerts package will integrate with the ECS systems to:
//   - Evaluate monitor state against alert policies
//   - Dispatch alerts via configured channels (PagerDuty, Slack, Email, Webhook)
//   - Track alert state and prevent duplicate notifications
//   - Support alert grouping and correlation
package alerts
