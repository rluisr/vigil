package model

type SLO struct {
	Name        string
	DisplayName string
	Goal        float64
	SLI         interface{}
}

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
