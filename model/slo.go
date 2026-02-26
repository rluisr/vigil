// Package model defines domain types for SLO analysis.
package model

// SLO represents a service level objective from a cloud provider.
type SLO struct {
	Name        string
	DisplayName string
	Goal        float64
	SLI         interface{}
}

// SLOData holds computed metrics for an SLO used in the Excel report.
type SLOData struct {
	Key        string
	Flag       bool
	TargetSLO  float64
	SLO        float64
	GoodQuery  string
	TotalQuery string
	AvgBudget  float64
	MinBudget  float64
}
