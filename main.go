package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	monitoringpb "cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"github.com/schollz/progressbar/v3"
	"github.com/xuri/excelize/v2"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const maxConcurrency = 16

var (
	projectID            = flag.String("project", "", "project id")
	errorBudgetThreshold = flag.Float64("error-budget-threshold", 0.9, "error budget threshold. 0 ~ 1") // Error budget threshold
	window               = flag.Duration("window", 720*time.Hour, "target window. use \"h\" suffix")
	wg                   sync.WaitGroup
	jobCh                = make(chan *monitoringpb.ServiceLevelObjective, 100000)
	resultCh             = make(chan jobResult, 100000)
	errCh                = make(chan error, 1)
	barMutex             sync.Mutex
	dataMutex            sync.RWMutex
)

type jobResult struct {
	key        string
	targetSLO  float64
	goodQuery  string
	totalQuery string
	min        float64
	avg        float64
	flag       bool
}

type sloData struct {
	Flag       bool
	SLO        float64
	GoodQuery  string
	TotalQuery string
	Avg        float64
	Min        float64
}

func main() {
	flag.Parse()
	if *projectID == "" {
		log.Panicf("--project id is required")
	}
	if *errorBudgetThreshold == 0 {
		log.Panicf("--error-budget-threshold is required")
	}
	if *errorBudgetThreshold <= 0 || *errorBudgetThreshold >= 1 {
		log.Panicf("--error-budget-threshold must be between 0 and 1")
	}

	ctx := context.Background()

	monitoringClient, err := monitoring.NewServiceMonitoringClient(ctx)
	if err != nil {
		log.Panicf("Failed to create monitoring client: %v", err)
	}
	defer monitoringClient.Close()

	metricClient, err := monitoring.NewMetricClient(ctx)
	if err != nil {
		log.Panicf("Failed to create metric client: %v", err)
	}
	defer metricClient.Close()

	var (
		slos []*monitoringpb.ServiceLevelObjective
		data = make(map[string]*sloData)
	)

	log.Printf("projectID: %s\n", *projectID)
	log.Printf("error budget threshold: %g%%\n", *errorBudgetThreshold*100)
	log.Printf("window: %g days\n", window.Hours()/24)

	services := monitoringClient.ListServices(ctx, &monitoringpb.ListServicesRequest{
		Parent: "projects/" + *projectID,
	})
	for {
		service, err := services.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			log.Panicf("Failed to list services: %v", err)
		}

		lSLOs := monitoringClient.ListServiceLevelObjectives(ctx, &monitoringpb.ListServiceLevelObjectivesRequest{
			Parent: service.GetName(),
		})
		for {
			slo, err := lSLOs.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				log.Panicf("Failed to list service level objectives: %v", err)
			}

			metrics, err := monitoringClient.GetServiceLevelObjective(ctx, &monitoringpb.GetServiceLevelObjectiveRequest{
				Name: slo.GetName(),
			})
			if err != nil {
				log.Panicf("Failed to get service level objective: %v", err)
			}

			slos = append(slos, metrics)
		}
	}

	bar := progressbar.Default(int64(len(slos)))
	var warnMessages []string

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// fetch data for each slo
	for i := 0; i < maxConcurrency; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for slo := range jobCh {
				select {
				case <-ctx.Done():
					return
				default:
					result, err := processSLO(ctx, metricClient, slo)
					if err != nil {
						if strings.Contains(err.Error(), "no data points found") {
							warnMessages = append(warnMessages, err.Error())
							continue
						}
						select {
						case errCh <- fmt.Errorf("SLO processing failed: %s: %w", slo.GetDisplayName(), err):
						default:
						}
						return
					}

					barMutex.Lock()
					err = bar.Add(1)
					if err != nil {
						log.Printf("Failed to add progress: %v", err)
					}
					barMutex.Unlock()

					resultCh <- result
				}
			}
		}()
	}

	go func() {
		for _, slo := range slos {
			select {
			case jobCh <- slo:
			case <-ctx.Done():
				return
			}
		}
		close(jobCh)
	}()

	go func() {
		for {
			select {
			case e, ok := <-errCh:
				if ok {
					cancel()
					err = bar.Finish()
					if err != nil {
						log.Printf("Failed to finish progress bar: %v", err)
					}
					log.Fatalf("Error in goroutine: %v", e)
				}
			case result, ok := <-resultCh:
				if !ok {
					return
				}

				dataMutex.Lock()

				data[result.key] = &sloData{
					Flag:       result.flag,
					SLO:        result.targetSLO,
					GoodQuery:  result.goodQuery,
					TotalQuery: result.totalQuery,
					Avg:        result.avg,
					Min:        result.min,
				}
				dataMutex.Unlock()
			}
		}
	}()

	wg.Wait()
	err = bar.Finish()
	if err != nil {
		log.Printf("Failed to finish progress bar: %v", err)
	}
	close(resultCh)
	close(errCh)

	f := excelize.NewFile()
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("Failed to close file: %v", err)
		}
	}()

	boldStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{
			Bold: true,
		},
	})
	if err != nil {
		log.Panicf("Failed to create style: %v", err)
	}

	highlightStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{
			Bold: true,
		},
		Fill: excelize.Fill{
			Type:    "pattern",
			Pattern: 1,
			Color:   []string{"21CE9C"},
		},
	})
	if err != nil {
		log.Panicf("Failed to create style: %v", err)
	}

	// description
	err = f.SetCellValue("Sheet1", "A1", fmt.Sprintf("SLO Report for %s\r\nList of SLOs that have never been below %g%% in %g days and should be adjusted.\r\n\r\nSLO Report: %s\r\n%g日間一度も %g%% を下回っていない調整すべき SLO リストです。", *projectID, *errorBudgetThreshold*100, window.Hours()/24, *projectID, window.Hours()/24, *errorBudgetThreshold*100))
	if err != nil {
		log.Panicf("Failed to set cell value: %v", err)
	}
	descriptionStyle, err := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{
			WrapText: true,
		},
		Font: &excelize.Font{
			Bold:  true,
			Color: "DE3163",
		},
	})
	if err != nil {
		log.Panicf("Failed to create style: %v", err)
	}
	err = f.SetCellStyle("Sheet1", "A1", "A1", descriptionStyle)
	if err != nil {
		log.Panicf("Failed to set cell style: %v", err)
	}

	// header
	err = f.SetCellValue("Sheet1", "A2", "Name")
	if err != nil {
		log.Panicf("Failed to set cell value: %v", err)
	}
	err = f.SetCellValue("Sheet1", "B2", "SLO")
	if err != nil {
		log.Panicf("Failed to set cell value: %v", err)
	}
	err = f.SetCellValue("Sheet1", "C2", "New SLO")
	if err != nil {
		log.Panicf("Failed to set cell value: %v", err)
	}
	err = f.SetCellValue("Sheet1", "D2", "SLI Min")
	if err != nil {
		log.Panicf("Failed to set cell value: %v", err)
	}
	err = f.SetCellValue("Sheet1", "E2", "SLI Avg")
	if err != nil {
		log.Panicf("Failed to set cell value: %v", err)
	}
	err = f.SetCellValue("Sheet1", "F2", "GoodQuery")
	if err != nil {
		log.Panicf("Failed to set cell value: %v", err)
	}
	err = f.SetCellValue("Sheet1", "G2", "TotalQuery")
	if err != nil {
		log.Panicf("Failed to set cell value: %v", err)
	}
	err = f.SetCellValue("Sheet1", "H2", "New GoodQuery?")
	if err != nil {
		log.Panicf("Failed to set cell value: %v", err)
	}
	err = f.SetCellValue("Sheet1", "I2", "New TotalQuery?")
	if err != nil {
		log.Panicf("Failed to set cell value: %v", err)
	}

	err = f.SetCellStyle("Sheet1", "A2", "I2", boldStyle)
	if err != nil {
		log.Panicf("Failed to set cell style: %v", err)
	}
	err = f.SetCellStyle("Sheet1", "C2", "C2", highlightStyle)
	if err != nil {
		log.Panicf("Failed to set cell style: %v", err)
	}

	// cell width
	err = f.SetColWidth("Sheet1", "A", "A", 50)
	if err != nil {
		log.Panicf("Failed to set cell width: %v", err)
	}
	err = f.SetColWidth("Sheet1", "B", "E", 10)
	if err != nil {
		log.Panicf("Failed to set cell width: %v", err)
	}
	err = f.SetColWidth("Sheet1", "F", "I", 50)
	if err != nil {
		log.Panicf("Failed to set cell width: %v", err)
	}

	i := 3
	for k, v := range data {
		// data
		err = f.SetCellValue("Sheet1", fmt.Sprintf("A%d", i), k)
		if err != nil {
			log.Panicf("Failed to set cell value: %v", err)
		}
		err = f.SetCellValue("Sheet1", fmt.Sprintf("B%d", i), v.SLO*100)
		if err != nil {
			log.Panicf("Failed to set cell value: %v", err)
		}
		err = f.SetCellValue("Sheet1", fmt.Sprintf("C%d", i), 0)
		if err != nil {
			log.Panicf("Failed to set cell value: %v", err)
		}
		err = f.SetCellValue("Sheet1", fmt.Sprintf("D%d", i), v.Min*100)
		if err != nil {
			log.Panicf("Failed to set cell value: %v", err)
		}
		err = f.SetCellValue("Sheet1", fmt.Sprintf("E%d", i), v.Avg*100)
		if err != nil {
			log.Panicf("Failed to set cell value: %v", err)
		}
		err = f.SetCellValue("Sheet1", fmt.Sprintf("F%d", i), v.GoodQuery)
		if err != nil {
			log.Panicf("Failed to set cell value: %v", err)
		}
		err = f.SetCellValue("Sheet1", fmt.Sprintf("G%d", i), v.TotalQuery)
		if err != nil {
			log.Panicf("Failed to set cell value: %v", err)
		}

		// style
		err = f.SetCellStyle("Sheet1", fmt.Sprintf("C%d", i), fmt.Sprintf("C%d", i), highlightStyle)
		if err != nil {
			log.Panicf("Failed to set cell style: %v", err)
		}

		i++
	}

	err = f.SetSheetView("Sheet1", 0, &excelize.ViewOptions{
		ShowGridLines: &[]bool{true}[0],
		ZoomScale:     &[]float64{150}[0],
	})
	if err != nil {
		log.Panicf("Failed to set sheet view: %v", err)
	}
	f.SetActiveSheet(0)

	err = f.SaveAs("slo_report.xlsx")
	if err != nil {
		log.Panicf("Failed to save file: %v", err)
	}

	for _, msg := range warnMessages {
		log.Println(msg)
	}

	log.Println("Report has been written to slo_report.xlsx")
}

func processSLO(ctx context.Context, client *monitoring.MetricClient, slo *monitoringpb.ServiceLevelObjective) (jobResult, error) {
	targetSLO := slo.GetGoal()
	sli := slo.GetServiceLevelIndicator()

	goodQuery := sli.GetRequestBased().GetGoodTotalRatio().GetGoodServiceFilter()
	totalQuery := sli.GetRequestBased().GetGoodTotalRatio().GetTotalServiceFilter()

	// range
	if goodQuery == "" || totalQuery == "" {
		goodQuery = sli.GetRequestBased().GetDistributionCut().GetRange().String()
		totalQuery = sli.GetRequestBased().GetDistributionCut().GetDistributionFilter()
	}

	result := jobResult{
		key:        slo.GetDisplayName(),
		targetSLO:  targetSLO,
		goodQuery:  goodQuery,
		totalQuery: totalQuery,
	}

	startTime := time.Now().UTC().Add(*window * -1).Unix()
	endTime := time.Now().UTC().Unix()

	req := &monitoringpb.ListTimeSeriesRequest{
		Name:   "projects/" + *projectID,
		Filter: fmt.Sprintf("select_slo_budget_fraction(%s)", slo.GetName()),
		Interval: &monitoringpb.TimeInterval{
			StartTime: &timestamppb.Timestamp{Seconds: startTime},
			EndTime:   &timestamppb.Timestamp{Seconds: endTime},
		},
	}

	iter := client.ListTimeSeries(ctx, req)
	var points []float64

	for {
		ts, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return jobResult{}, fmt.Errorf("failed to get time series: %w", err)
		}

		for _, point := range ts.GetPoints() {
			value := point.GetValue().GetDoubleValue()
			if value <= *errorBudgetThreshold {
				result.flag = true
				break
			}
			points = append(points, value)
		}
	}

	if len(points) == 0 {
		return jobResult{}, fmt.Errorf("no data points found for SLO: %s", slo.GetDisplayName())
	}

	result.min, result.avg = getMinAvgErrorBudget(points)
	return result, nil
}

func getMinAvgErrorBudget(points []float64) (minValue float64, avgValue float64) {
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
