package main

import (
	"context"

	"github.com/rluisr/vigil/model"
)

type Vigil interface {
	GetProvider() model.CloudProvider
	GetSLOs(ctx context.Context) ([]*model.SLO, error)
	GetErrorBudgetTimeSeries(ctx context.Context, slo *model.SLO) (good string, total string, points []float64, err error)
}
