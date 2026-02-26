// Package datadog provides a Datadog SLO client implementing the Vigil interface.
package datadog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
	datadogV1 "github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
	"github.com/rluisr/vigil/model"
)

// Client is a Datadog SLO API client.
type Client struct {
	api                  *datadogV1.ServiceLevelObjectivesApi
	ctx                  context.Context
	ErrorBudgetThreshold float64
	Window               time.Duration
}

// NewClient creates a new Datadog client. Requires DD_API_KEY and DD_APP_KEY environment variables.
func NewClient(ctx context.Context, ddSite string, errorBudgetThreshold float64, window time.Duration) (*Client, error) {
	if _, ok := os.LookupEnv("DD_API_KEY"); !ok {
		return nil, errors.New("DD_API_KEY environment variable is required")
	}
	if _, ok := os.LookupEnv("DD_APP_KEY"); !ok {
		return nil, errors.New("DD_APP_KEY environment variable is required")
	}

	ctx = datadog.NewDefaultContext(ctx)
	if ddSite != "" {
		ctx = context.WithValue(ctx, datadog.ContextServerVariables, map[string]string{"site": ddSite})
	}

	cfg := datadog.NewConfiguration()
	apiClient := datadog.NewAPIClient(cfg)
	api := datadogV1.NewServiceLevelObjectivesApi(apiClient)

	return &Client{
		api:                  api,
		ctx:                  ctx,
		ErrorBudgetThreshold: errorBudgetThreshold,
		Window:               window,
	}, nil
}

// GetProvider returns the Datadog cloud provider identifier.
func (c *Client) GetProvider() model.CloudProvider {
	return model.CloudProviderDD
}

// GetSLOs retrieves all SLOs from the Datadog API with pagination.
func (c *Client) GetSLOs(_ context.Context) ([]*model.SLO, error) {
	var slos []*model.SLO

	ch, cancel := c.api.ListSLOsWithPagination(c.ctx, *datadogV1.NewListSLOsOptionalParameters().WithLimit(100))
	defer cancel()

	for result := range ch {
		if result.Error != nil {
			return nil, fmt.Errorf("failed to list SLOs: %w", result.Error)
		}
		slo := result.Item

		thresholds := slo.GetThresholds()
		var goal float64
		if len(thresholds) > 0 {
			goal = thresholds[0].GetTarget() / 100.0
		}

		slos = append(slos, &model.SLO{
			Name:        slo.GetId(),
			DisplayName: slo.GetName(),
			Goal:        goal,
			SLI:         slo,
		})
	}

	return slos, nil
}

// GetErrorBudgetTimeSeries fetches error budget time series data for a given SLO.
func (c *Client) GetErrorBudgetTimeSeries(_ context.Context, slo *model.SLO) (string, string, []float64, error) {
	ddSLO, ok := slo.SLI.(datadogV1.ServiceLevelObjective)
	if !ok {
		return "", "", nil, fmt.Errorf("SLI is not of expected type: %T", slo.SLI)
	}

	fromTs := time.Now().UTC().Add(c.Window * -1).Unix()
	toTs := time.Now().UTC().Unix()

	resp, _, err := c.api.GetSLOHistory(c.ctx, slo.Name, fromTs, toTs, *datadogV1.NewGetSLOHistoryOptionalParameters().WithApplyCorrection(true))
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to get SLO history: %w", err)
	}

	data := resp.GetData()

	var (
		good   string
		total  string
		points []float64
	)

	sloType := ddSLO.GetType()
	switch sloType {
	case datadogV1.SLOTYPE_METRIC:
		good, total, points = processMetricSLO(data, ddSLO)
	case datadogV1.SLOTYPE_MONITOR, datadogV1.SLOTYPE_TIME_SLICE:
		good, total, points = processMonitorSLO(data, ddSLO)
	default:
		return "", "", nil, fmt.Errorf("unsupported SLO type: %s", sloType)
	}

	if len(points) == 0 {
		return "", "", nil, fmt.Errorf("no data points found for SLO: %s", slo.DisplayName)
	}

	return good, total, points, nil
}

func processMetricSLO(data datadogV1.SLOHistoryResponseData, ddSLO datadogV1.ServiceLevelObjective) (string, string, []float64) {
	query := ddSLO.GetQuery()
	good := query.GetNumerator()
	total := query.GetDenominator()

	series := data.GetSeries()
	numerator := series.GetNumerator()
	denominator := series.GetDenominator()

	numValues := numerator.GetValues()
	denValues := denominator.GetValues()

	var points []float64
	for i := range numValues {
		if i >= len(denValues) {
			break
		}
		if denValues[i] == 0 {
			continue
		}
		points = append(points, numValues[i]/denValues[i])
	}

	return good, total, points
}

func processMonitorSLO(data datadogV1.SLOHistoryResponseData, ddSLO datadogV1.ServiceLevelObjective) (string, string, []float64) {
	good := fmt.Sprintf("monitor_ids: %v", ddSLO.GetMonitorIds())
	total := fmt.Sprintf("type: %s", ddSLO.GetType())

	overall := data.GetOverall()
	history := overall.GetHistory()

	var (
		points      []float64
		uptimeCount float64
		totalCount  float64
	)

	for _, entry := range history {
		if len(entry) < 2 {
			continue
		}
		state := entry[1]
		if state == 2 { // no-data, skip
			continue
		}
		totalCount++
		if state == 0 { // uptime
			uptimeCount++
		}
		// state == 1: downtime
		if totalCount > 0 {
			points = append(points, uptimeCount/totalCount)
		}
	}

	return good, total, points
}
