const conditions: Record<string, string> = {
  healthy: 'Targets met in the measured window',
  insufficient_observations: 'Insufficient observations',
  queue_delay: 'Scheduling or queue delay exceeds its target',
  result_delay: 'Scheduled-to-result delay exceeds its target',
  downstream_limited: 'Target execution is too slow for the latency target',
  controller_limited: 'Controller progress limits latency; more workers may not help',
  coverage_gap: 'Measurement coverage is incomplete',
  disabled: 'Latency feedback is disabled',
};
export function conditionLabel(value?: string): string {
  return value && Object.prototype.hasOwnProperty.call(conditions, value) ? conditions[value] : value ? 'Unrecognized condition' : 'Not reported';
}
