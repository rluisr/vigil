package utils

import "math"

func GetMinAvgErrorBudget(points []float64) (minValue float64, avgValue float64) {
	if len(points) == 0 {
		return 0, 0
	}

	minValue = math.Inf(1)
	avgValue = 0.0
	for _, point := range points {
		if point < minValue {
			minValue = point
		}
		avgValue += point
	}
	return minValue, avgValue / float64(len(points))
}

func IsPercentNegative(data []float64, percent float64) bool {
	if percent < 0 || percent > 1 {
		return false
	}

	if len(data) == 0 {
		return false
	}

	negativeCount := 0
	for _, num := range data {
		if num < 0 {
			negativeCount++
		}
	}

	return float64(negativeCount)/float64(len(data)) >= percent
}
