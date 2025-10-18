package jobs

import (
	"testing"
)

// TestIsNilInterface verifies that IsNil() correctly handles the typed-nil problem
func TestIsNilInterface(t *testing.T) {
	tests := []struct {
		job     Job
		name    string
		wantNil bool
	}{
		{
			name:    "nil interface",
			job:     nil,
			wantNil: true,
		},
		{
			name:    "nil *PulseHTTPJob",
			job:     (*PulseHTTPJob)(nil),
			wantNil: true,
		},
		{
			name:    "nil *PulseTCPJob",
			job:     (*PulseTCPJob)(nil),
			wantNil: true,
		},
		{
			name:    "nil *PulseICMPJob",
			job:     (*PulseICMPJob)(nil),
			wantNil: true,
		},
		{
			name:    "nil *InterventionDockerJob",
			job:     (*InterventionDockerJob)(nil),
			wantNil: true,
		},
		{
			name:    "nil *CodeLogJob",
			job:     (*CodeLogJob)(nil),
			wantNil: true,
		},
		{
			name:    "nil *CodePagerDutyJob",
			job:     (*CodePagerDutyJob)(nil),
			wantNil: true,
		},
		{
			name:    "nil *CodeSlackJob",
			job:     (*CodeSlackJob)(nil),
			wantNil: true,
		},
		{
			name:    "nil *CodeEmailJob",
			job:     (*CodeEmailJob)(nil),
			wantNil: true,
		},
		{
			name:    "nil *CodeWebhookJob",
			job:     (*CodeWebhookJob)(nil),
			wantNil: true,
		},
		{
			name: "non-nil PulseHTTPJob",
			job: &PulseHTTPJob{
				URL:    "http://example.com",
				Method: "GET",
			},
			wantNil: false,
		},
		{
			name: "non-nil CodeLogJob",
			job: &CodeLogJob{
				File: "test.log",
			},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test direct method call
			if tt.job == nil {
				// Can't call method on nil interface
				return
			}

			got := tt.job.IsNil()
			if got != tt.wantNil {
				t.Errorf("Job.IsNil() = %v, want %v", got, tt.wantNil)
			}
		})
	}
}

// TestIsNilHelperFunction tests the helper function pattern used in adaptive_queue.go and batch_code_system.go
func TestIsNilHelperFunction(t *testing.T) {
	isNilJob := func(job Job) bool {
		return job == nil || job.IsNil()
	}

	tests := []struct {
		job     Job
		name    string
		wantNil bool
	}{
		{
			name:    "nil interface",
			job:     nil,
			wantNil: true,
		},
		{
			name:    "typed nil pointer",
			job:     (*PulseHTTPJob)(nil),
			wantNil: true,
		},
		{
			name: "non-nil job",
			job: &PulseHTTPJob{
				URL:    "http://example.com",
				Method: "GET",
			},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNilJob(tt.job)
			if got != tt.wantNil {
				t.Errorf("isNilJob() = %v, want %v", got, tt.wantNil)
			}
		})
	}
}
