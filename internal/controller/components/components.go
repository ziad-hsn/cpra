package components

import (
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"errors"
	"time"
)

type Name string

type DisabledMonitor struct{}

type MonitorStatus struct {
	Status          string
	LastCheckTime   time.Time
	LastSuccessTime time.Time
	LastError       error
}

// Copy creates a deep copy of the MonitorStatus.
func (s *MonitorStatus) Copy() *MonitorStatus {
	if s == nil {
		return nil
	}
	cpy := &MonitorStatus{
		Status:          s.Status,
		LastCheckTime:   s.LastCheckTime,
		LastSuccessTime: s.LastSuccessTime,
	}
	// Deep copy the error to prevent dangling pointers.
	if s.LastError != nil {
		cpy.LastError = errors.New(s.LastError.Error())
	}
	return cpy
}

type PulseConfig struct {
	Type        string
	Timeout     time.Duration
	Interval    time.Duration
	Retries     int
	MaxFailures int
	Config      schema.PulseConfig
}

// Copy Fixed Copy methods with deep copy implementations:
func (c *PulseConfig) Copy() *PulseConfig {
	if c == nil {
		return nil
	}
	cpy := &PulseConfig{
		Type:        c.Type,
		Timeout:     c.Timeout,
		Interval:    c.Interval,
		Retries:     c.Retries,
		MaxFailures: c.MaxFailures,
	}

	if c.Config != nil {
		// Deep copy based on concrete type
		cpy.Config = *new(schema.PulseConfig)
		cpy.Config = c.Config.Copy()
	}
	return cpy
}

type Pulse struct {
	Config PulseConfig
	Status PulseStatus
}
type PulseFirstCheck struct{}
type PulseNeeded struct{}
type PulsePending struct{}
type PulseFailed struct{}
type PulseSuccess struct{}

type PulseJob struct {
	Job jobs.Job
}

type PulseResults struct {
	Results jobs.Result
}

type PulseStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastCheckTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *PulseStatus) Copy() *PulseStatus {
	if s == nil {
		return nil
	}
	cpy := &PulseStatus{
		LastStatus:          s.LastStatus,
		ConsecutiveFailures: s.ConsecutiveFailures,
		LastCheckTime:       s.LastCheckTime,
		LastSuccessTime:     s.LastSuccessTime,
	}
	if s.LastError != nil {
		cpy.LastError = errors.New(s.LastError.Error())
	}
	return cpy
}

type InterventionNeeded struct{}
type CodeNeeded struct {
	Color string
}

type InterventionConfig struct {
	Action      string
	MaxFailures int
	Target      schema.InterventionTarget
}

func (c *InterventionConfig) Copy() *InterventionConfig {
	if c == nil {
		return nil
	}
	cpy := &InterventionConfig{
		Action:      c.Action,
		MaxFailures: c.MaxFailures,
	}

	if c.Target != nil {
		// Deep copy based on concrete type
		cpy.Target = *new(schema.InterventionTarget)
		cpy.Target = c.Target.Copy()
	}
	return cpy
}

type InterventionPending struct{}
type InterventionFailed struct{}
type InterventionSuccess struct{}

type InterventionJob struct {
	Job jobs.Job
}

type InterventionResults struct {
	Results jobs.Result
}

type InterventionStatus struct {
	LastStatus           string
	ConsecutiveFailures  int
	LastInterventionTime time.Time
	LastSuccessTime      time.Time
	LastError            error
}

// Copy creates a deep copy of the InterventionStatus.
func (s *InterventionStatus) Copy() *InterventionStatus {
	if s == nil {
		return nil
	}
	cpy := &InterventionStatus{
		LastStatus:           s.LastStatus,
		ConsecutiveFailures:  s.ConsecutiveFailures,
		LastInterventionTime: s.LastInterventionTime,
		LastSuccessTime:      s.LastSuccessTime,
	}
	if s.LastError != nil {
		cpy.LastError = errors.New(s.LastError.Error())
	}
	return cpy
}

type CodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}

// Copy Apply similar deep copy pattern to all CodeConfig types:
func (c *CodeConfig) Copy() *CodeConfig {
	if c == nil {
		return nil
	}
	cpy := &CodeConfig{
		Dispatch:    c.Dispatch,
		MaxFailures: c.MaxFailures,
		Notify:      c.Notify,
	}
	if c.Config != nil {
		// Deep copy based on concrete type
		cpy.Config = *new(schema.CodeNotification)
		cpy.Config = c.Config.Copy()
	}
	return cpy
}

type CodePending struct {
	Color string
}

type CodeJob struct {
	Job jobs.Job
}

type CodeResults struct {
	Results jobs.Result
}

type CodeStatusAccessor interface {
	SetSuccess(t time.Time)
	SetFailure(err error)
}

// Marker/tag components
type RedCode struct{}

type RedCodeJob struct {
	Job jobs.Job
}
type RedCodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}

// Copy Apply similar deep copy pattern to all CodeConfig types:
func (c *RedCodeConfig) Copy() *RedCodeConfig {
	if c == nil {
		return nil
	}
	cpy := &RedCodeConfig{
		Dispatch:    c.Dispatch,
		MaxFailures: c.MaxFailures,
		Notify:      c.Notify,
	}
	if c.Config != nil {
		// Deep copy based on concrete type
		switch v := c.Config.(type) {
		case *schema.CodeNotificationLog:
			cpy.Config = &schema.CodeNotificationLog{File: v.File}
		case *schema.CodeNotificationPagerDuty:
			cpy.Config = &schema.CodeNotificationPagerDuty{URL: v.URL}
		case *schema.CodeNotificationSlack:
			cpy.Config = &schema.CodeNotificationSlack{WebHook: v.WebHook}
		
		}
	}
	return cpy
}

type RedCodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *RedCodeStatus) SetSuccess(t time.Time) {
	s.LastStatus = "success"
	s.LastError = nil
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t
	s.LastAlertTime = t
}

func (s *RedCodeStatus) SetFailure(err error) {
	s.LastStatus = "failed"
	s.LastError = err
	s.ConsecutiveFailures++
}

// Copy creates a deep copy of the RedCodeStatus.
func (s *RedCodeStatus) Copy() *RedCodeStatus {
	if s == nil {
		return nil
	}
	cpy := &RedCodeStatus{
		LastStatus:          s.LastStatus,
		ConsecutiveFailures: s.ConsecutiveFailures,
		LastAlertTime:       s.LastAlertTime,
		LastSuccessTime:     s.LastSuccessTime,
	}
	if s.LastError != nil {
		cpy.LastError = errors.New(s.LastError.Error())
	}
	return cpy
}

type GreenCode struct{}

type GreenCodeJob struct {
	Job jobs.Job
}
type GreenCodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}

// Copy Apply similar deep copy pattern to all CodeConfig types:
func (c *GreenCodeConfig) Copy() *GreenCodeConfig {
	if c == nil {
		return nil
	}
	cpy := &GreenCodeConfig{
		Dispatch:    c.Dispatch,
		MaxFailures: c.MaxFailures,
		Notify:      c.Notify,
	}
	if c.Config != nil {
		// Deep copy based on concrete type
		switch v := c.Config.(type) {
		case *schema.CodeNotificationLog:
			cpy.Config = &schema.CodeNotificationLog{File: v.File}
		case *schema.CodeNotificationPagerDuty:
			cpy.Config = &schema.CodeNotificationPagerDuty{URL: v.URL}
		case *schema.CodeNotificationSlack:
			cpy.Config = &schema.CodeNotificationSlack{WebHook: v.WebHook}
		}
	}
	return cpy
}

type GreenCodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *GreenCodeStatus) SetSuccess(t time.Time) {
	s.LastStatus = "success"
	s.LastError = nil
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t
	s.LastAlertTime = t
}

func (s *GreenCodeStatus) SetFailure(err error) {
	s.LastStatus = "failed"
	s.LastError = err
	s.ConsecutiveFailures++
}

// Copy creates a deep copy of the GreenCodeStatus.
func (s *GreenCodeStatus) Copy() *GreenCodeStatus {
	if s == nil {
		return nil
	}
	cpy := &GreenCodeStatus{
		LastStatus:          s.LastStatus,
		ConsecutiveFailures: s.ConsecutiveFailures,
		LastAlertTime:       s.LastAlertTime,
		LastSuccessTime:     s.LastSuccessTime,
	}
	if s.LastError != nil {
		cpy.LastError = errors.New(s.LastError.Error())
	}
	return cpy
}

type CyanCode struct{}

type CyanCodeJob struct {
	Job jobs.Job
}
type CyanCodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}

// Copy Apply similar deep copy pattern to all CodeConfig types:
func (c *CyanCodeConfig) Copy() *CyanCodeConfig {
	if c == nil {
		return nil
	}
	cpy := &CyanCodeConfig{
		Dispatch:    c.Dispatch,
		MaxFailures: c.MaxFailures,
		Notify:      c.Notify,
	}
	if c.Config != nil {
		// Deep copy based on concrete type
		switch v := c.Config.(type) {
		case *schema.CodeNotificationLog:
			cpy.Config = &schema.CodeNotificationLog{File: v.File}
		case *schema.CodeNotificationPagerDuty:
			cpy.Config = &schema.CodeNotificationPagerDuty{URL: v.URL}
		case *schema.CodeNotificationSlack:
			cpy.Config = &schema.CodeNotificationSlack{WebHook: v.WebHook}
		}
	}
	return cpy
}

type CyanCodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *CyanCodeStatus) SetSuccess(t time.Time) {
	s.LastStatus = "success"
	s.LastError = nil
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t
	s.LastAlertTime = t
}

func (s *CyanCodeStatus) SetFailure(err error) {
	s.LastStatus = "failed"
	s.LastError = err
	s.ConsecutiveFailures++
}

// Copy creates a deep copy of the CyanCodeStatus.
func (s *CyanCodeStatus) Copy() *CyanCodeStatus {
	if s == nil {
		return nil
	}
	cpy := &CyanCodeStatus{
		LastStatus:          s.LastStatus,
		ConsecutiveFailures: s.ConsecutiveFailures,
		LastAlertTime:       s.LastAlertTime,
		LastSuccessTime:     s.LastSuccessTime,
	}
	if s.LastError != nil {
		cpy.LastError = errors.New(s.LastError.Error())
	}
	return cpy
}

type YellowCode struct{}

type YellowCodeJob struct {
	Job jobs.Job
}
type YellowCodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}

// Copy Apply similar deep copy pattern to all CodeConfig types:
func (c *YellowCodeConfig) Copy() *YellowCodeConfig {
	if c == nil {
		return nil
	}
	cpy := &YellowCodeConfig{
		Dispatch:    c.Dispatch,
		MaxFailures: c.MaxFailures,
		Notify:      c.Notify,
	}
	if c.Config != nil {
		// Deep copy based on concrete type
		switch v := c.Config.(type) {
		case *schema.CodeNotificationLog:
			cpy.Config = &schema.CodeNotificationLog{File: v.File}
		case *schema.CodeNotificationPagerDuty:
			cpy.Config = &schema.CodeNotificationPagerDuty{URL: v.URL}
		case *schema.CodeNotificationSlack:
			cpy.Config = &schema.CodeNotificationSlack{WebHook: v.WebHook}
		}
	}
	return cpy
}

type YellowCodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *YellowCodeStatus) SetSuccess(t time.Time) {
	s.LastStatus = "success"
	s.LastError = nil
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t
	s.LastAlertTime = t
}

func (s *YellowCodeStatus) SetFailure(err error) {
	s.LastStatus = "failed"
	s.LastError = err
	s.ConsecutiveFailures++
}

// Copy creates a deep copy of the YellowCodeStatus.
func (s *YellowCodeStatus) Copy() *YellowCodeStatus {
	if s == nil {
		return nil
	}
	cpy := &YellowCodeStatus{
		LastStatus:          s.LastStatus,
		ConsecutiveFailures: s.ConsecutiveFailures,
		LastAlertTime:       s.LastAlertTime,
		LastSuccessTime:     s.LastSuccessTime,
	}
	if s.LastError != nil {
		cpy.LastError = errors.New(s.LastError.Error())
	}
	return cpy
}

type GrayCode struct{}

type GrayCodeJob struct {
	Job jobs.Job
}
type GrayCodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}

// Copy Apply similar deep copy pattern to all CodeConfig types:
func (c *GrayCodeConfig) Copy() *GrayCodeConfig {
	if c == nil {
		return nil
	}
	cpy := &GrayCodeConfig{
		Dispatch:    c.Dispatch,
		MaxFailures: c.MaxFailures,
		Notify:      c.Notify,
	}
	if c.Config != nil {
		// Deep copy based on concrete type
		switch v := c.Config.(type) {
		case *schema.CodeNotificationLog:
			cpy.Config = &schema.CodeNotificationLog{File: v.File}
		case *schema.CodeNotificationPagerDuty:
			cpy.Config = &schema.CodeNotificationPagerDuty{URL: v.URL}
		case *schema.CodeNotificationSlack:
			cpy.Config = &schema.CodeNotificationSlack{WebHook: v.WebHook}
		}
	}
	return cpy
}

type GrayCodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *GrayCodeStatus) SetSuccess(t time.Time) {
	s.LastStatus = "success"
	s.LastError = nil
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t
	s.LastAlertTime = t
}

func (s *GrayCodeStatus) SetFailure(err error) {
	s.LastStatus = "failed"
	s.LastError = err
	s.ConsecutiveFailures++
}

// Copy creates a deep copy of the GrayCodeStatus.
func (s *GrayCodeStatus) Copy() *GrayCodeStatus {
	if s == nil {
		return nil
	}
	cpy := &GrayCodeStatus{
		LastStatus:          s.LastStatus,
		ConsecutiveFailures: s.ConsecutiveFailures,
		LastAlertTime:       s.LastAlertTime,
		LastSuccessTime:     s.LastSuccessTime,
	}
	if s.LastError != nil {
		cpy.LastError = errors.New(s.LastError.Error())
	}
	return cpy
}
