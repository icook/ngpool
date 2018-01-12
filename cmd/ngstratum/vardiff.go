package main

import "math"

type VarDiff struct {
	targetSubmissionRate float64
	tiers                []float64
}

func NewVarDiff(min float64, max float64, target float64) *VarDiff {
	if max < min {
		panic("Min must be less than max")
	}
	var tiers []float64
	for {
		tiers = append(tiers, min)
		min *= 2
		if min >= max {
			tiers = append(tiers, max)
			break
		}
	}
	return &VarDiff{
		targetSubmissionRate: target,
		tiers:                tiers,
	}
}

func (v *VarDiff) ComputeNew(currentDiff float64, shareRate float64) float64 {
	if len(v.tiers) == 1 {
		return v.tiers[1]
	}
	idealNew := shareRate / v.targetSubmissionRate
	var smallestDiff = math.Inf(1)
	var newDiff float64
	for _, tier := range v.tiers {
		gap := math.Abs(tier - idealNew)
		if gap < smallestDiff {
			smallestDiff = gap
			newDiff = tier
		}
		if gap > smallestDiff {
			break
		}
	}
	return newDiff
}
