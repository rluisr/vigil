// Package gcp provides a GCP Cloud Monitoring SLO client implementing the Vigil interface.
package gcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"github.com/rluisr/vigil/model"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Client is a GCP Cloud Monitoring SLO client.
type Client struct {
	MonitoringClient     *monitoring.ServiceMonitoringClient
	MetricClient         *monitoring.MetricClient
	GCPProjectID         string
	ErrorBudgetThreshold float64
	Window               time.Duration
}

// NewClient creates a new GCP monitoring client.
func NewClient(ctx context.Context, gcpProjectID string, errorBudgetThreshold float64, window time.Duration) (*Client, error) {
	monitoringClient, err := monitoring.NewServiceMonitoringClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create monitoring client: %w", err)
	}

	metricClient, err := monitoring.NewMetricClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create metric client: %w", err)
	}

	return &Client{
		MonitoringClient:     monitoringClient,
		MetricClient:         metricClient,
		GCPProjectID:         gcpProjectID,
		ErrorBudgetThreshold: errorBudgetThreshold,
		Window:               window,
	}, nil
}

// GetProvider returns the GCP cloud provider identifier.
func (c *Client) GetProvider() model.CloudProvider {
	return model.CloudProviderGCP
}

// GetSLOs retrieves all SLOs from GCP Cloud Monitoring.
func (c *Client) GetSLOs(ctx context.Context) ([]*model.SLO, error) {
	var slos []*model.SLO

	services := c.MonitoringClient.ListServices(ctx, &monitoringpb.ListServicesRequest{
		Parent: "projects/" + c.GCPProjectID,
	})
	for {
		service, err := services.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list services: %w", err)
		}

		lSLOs := c.MonitoringClient.ListServiceLevelObjectives(ctx, &monitoringpb.ListServiceLevelObjectivesRequest{
			Parent: service.GetName(),
		})
		for {
			slo, err := lSLOs.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("failed to list service level objectives: %w", err)
			}

			metrics, err := c.MonitoringClient.GetServiceLevelObjective(ctx, &monitoringpb.GetServiceLevelObjectiveRequest{
				Name: slo.GetName(),
			})
			if err != nil {
				return nil, fmt.Errorf("failed to get service level objective: %w", err)
			}

			slos = append(slos, &model.SLO{
				Name:        metrics.GetName(),
				DisplayName: metrics.GetDisplayName(),
				Goal:        metrics.GetGoal(),
				SLI:         metrics.GetServiceLevelIndicator(),
			})
		}
	}

	return slos, nil
}

// GetErrorBudgetTimeSeries fetches error budget time series data for a given SLO.
func (c *Client) GetErrorBudgetTimeSeries(ctx context.Context, slo *model.SLO) (good string, total string, points []float64, err error) {
	sli, ok := slo.SLI.(*monitoringpb.ServiceLevelIndicator)
	if !ok {
		return "", "", nil, fmt.Errorf("is not of expected type: %T", slo)
	}

	goodQuery := sli.GetRequestBased().GetGoodTotalRatio().GetGoodServiceFilter()
	totalQuery := sli.GetRequestBased().GetGoodTotalRatio().GetTotalServiceFilter()

	// range
	if goodQuery == "" || totalQuery == "" {
		goodQuery = sli.GetRequestBased().GetDistributionCut().GetRange().String()
		totalQuery = sli.GetRequestBased().GetDistributionCut().GetDistributionFilter()
	}

	startTime := time.Now().UTC().Add(c.Window * -1).Unix()
	endTime := time.Now().UTC().Unix()

	req := &monitoringpb.ListTimeSeriesRequest{
		Name:   "projects/" + c.GCPProjectID,
		Filter: fmt.Sprintf("select_slo_budget_fraction(%s)", slo.Name),
		Interval: &monitoringpb.TimeInterval{
			StartTime: &timestamppb.Timestamp{Seconds: startTime},
			EndTime:   &timestamppb.Timestamp{Seconds: endTime},
		},
	}

	iter := c.MetricClient.ListTimeSeries(ctx, req)

	for {
		ts, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to get time series: %w", err)
		}

		for _, point := range ts.GetPoints() {
			value := point.GetValue().GetDoubleValue()
			points = append(points, value)
		}
	}

	if len(points) == 0 {
		return "", "", nil, fmt.Errorf("no data points found for SLO: %s", slo.DisplayName)
	}

	return goodQuery, totalQuery, points, nil
}
