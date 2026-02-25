// Package constants provides legacy aliases for pkg/consts.
// This file will be removed after import paths are updated.
package constants

import (
	"cpra/pkg/consts"
)

// Re-export all constants from pkg/consts
const (
	DefaultHealthPort = consts.DefaultHealthPort
	DefaultPProfPort  = consts.DefaultPProfPort
	DefaultOTLPPort   = consts.DefaultOTLPPort
	MaxValidPort      = consts.MaxValidPort
	MinValidPort      = consts.MinValidPort

	DefaultResultBatchSize   = consts.DefaultResultBatchSize
	DefaultChannelDepth      = consts.DefaultChannelDepth
	DefaultWorkerMemBudget   = consts.DefaultWorkerMemBudget
	MinWorkerMemBudget       = consts.MinWorkerMemBudget
	DefaultMaxWorkersLinux   = consts.DefaultMaxWorkersLinux
	DefaultMaxWorkersWindows = consts.DefaultMaxWorkersWindows
	DefaultWorkerPerCore     = consts.DefaultWorkerPerCore
	MaxIdleConnsPerHost      = consts.MaxIdleConnsPerHost

	DefaultRingCapacity        = consts.DefaultRingCapacity
	DefaultOverflowCapacity    = consts.DefaultOverflowCapacity
	SoftWatermark              = consts.SoftWatermark
	HardWatermark              = consts.HardWatermark
	OverflowShrinkRatio        = consts.OverflowShrinkRatio
	MinOverflowBeforeShrink    = consts.MinOverflowBeforeShrink

	DefaultTargetLatency      = consts.DefaultTargetLatency
	DefaultScaleDownCooldown  = consts.DefaultScaleDownCooldown
	DefaultScaleUpCooldown    = consts.DefaultScaleUpCooldown
	HTTPConnTimeout           = consts.HTTPConnTimeout
	DefaultBatchTimeout       = consts.DefaultBatchTimeout
	DefaultAdjustmentInterval = consts.DefaultAdjustmentInterval
	DefaultWarmupDuration     = consts.DefaultWarmupDuration
	DefaultExpiryDuration     = consts.DefaultExpiryDuration

	DefaultRetryBackoff = consts.DefaultRetryBackoff
	MaxRetryBackoff     = consts.MaxRetryBackoff
	DefaultJobTimeout   = consts.DefaultJobTimeout

	DefaultScaleUpThreshold   = consts.DefaultScaleUpThreshold
	DefaultScaleDownThreshold = consts.DefaultScaleDownThreshold
	DefaultHeadroom           = consts.DefaultHeadroom
	InterventionPoolRatio     = consts.InterventionPoolRatio
	CodePoolRatio             = consts.CodePoolRatio
	HighUtilizationThreshold  = consts.HighUtilizationThreshold
	LowUtilizationThreshold   = consts.LowUtilizationThreshold
	MinServiceTime            = consts.MinServiceTime

	StatusOK                 = consts.StatusOK
	StatusNotFound           = consts.StatusNotFound
	StatusInternalError      = consts.StatusInternalError
	StatusServiceUnavailable = consts.StatusServiceUnavailable

	MaxShardSlots     = consts.MaxShardSlots
	DefaultShardSlots = consts.DefaultShardSlots
)