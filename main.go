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
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const maxConcurrency = 16

var (
	projectID            = flag.String("project", "", "project id")
	errorBudgetThreshold = flag.Float64("error-budget-threshold", 0.9, "error budget threshold. 0 ~ 1") // Error budget threshold
	window               = flag.Duration("window", 720*time.Hour, "target window. use \"h\" suffix")
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
	validateFlags()

	ctx := context.Background()
	monitoringClient := createClient(ctx, monitoring.NewServiceMonitoringClient)
	metricClient := createClient(ctx, monitoring.NewMetricClient)

	slos := listSLOs(ctx, monitoringClient)
	data := processSLOs(ctx, metricClient, slos)

	generateExcelReport(data)
	log.Println("Report has been written to slo_report.xlsx")
}

func createClient[T interface{ Close() error }](ctx context.Context, factory func(ctx context.Context, opts ...option.ClientOption) (T, error)) T {
	client, err := factory(ctx)
	handleError(err, "Failed to create client")
	return client
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

func validateFlags() {
	if *projectID == "" {
		log.Panicf("--project id is required")
	}
	if *errorBudgetThreshold <= 0 || *errorBudgetThreshold >= 1 {
		log.Panicf("--error-budget-threshold must be between 0 and 1")
	}
	if *window <= 0 {
		log.Panicf("--window must be positive duration")
	}
}

func generateExcelReport(data map[string]*sloData) {
	f := excelize.NewFile()
	defer func() {
		err := f.Close()
		if err != nil {
			log.Printf("Failed to close file: %v", err)
		}
	}()

	// スタイル作成をヘルパー関数化
	boldStyle := createStyle(f, &excelize.Font{Bold: true})
	highlightStyle := createStyle(f, &excelize.Font{Bold: true}, excelize.Fill{
		Type:    "pattern",
		Pattern: 1,
		Color:   []string{"21CE9C"},
	})
	descriptionStyle := createStyle(f, &excelize.Font{
		Bold:  true,
		Color: "DE3163",
	}, excelize.Alignment{WrapText: true})

	setColWidth(f, "Sheet1", map[string]float64{
		"A":   50,
		"B-E": 10,
		"F-I": 50,
	})
	setSheetView(f)
	setCellWithStyle(f, "A1", fmt.Sprintf("SLO Report for %s\nList of SLOs that have never been below %g%% in %g days...",
		*projectID, *errorBudgetThreshold*100, window.Hours()/24), descriptionStyle)
	setCellWithStyle(f, "C2", "New SLO", highlightStyle)

	headers := []string{"Name", "SLO", "New SLO", "SLI Min", "SLI Avg", "GoodQuery", "TotalQuery", "New GoodQuery?", "New TotalQuery?"}
	for i, h := range headers {
		setCellWithStyle(f, fmt.Sprintf("%c2", 'A'+i), h, boldStyle)
	}

	// データ設定
	row := 3
	for k, v := range data {
		setCellValue(f, fmt.Sprintf("A%d", row), k)
		setCellValue(f, fmt.Sprintf("B%d", row), v.SLO*100)
		setCellWithStyle(f, fmt.Sprintf("C%d", row), 0, highlightStyle)
		setCellValue(f, fmt.Sprintf("D%d", row), v.Min*100)
		setCellValue(f, fmt.Sprintf("E%d", row), v.Avg*100)
		setCellValue(f, fmt.Sprintf("F%d", row), v.GoodQuery)
		setCellValue(f, fmt.Sprintf("G%d", row), v.TotalQuery)
		row++
	}

	setCellWithStyle(f, "C2", "New SLO", highlightStyle)

	err := f.SaveAs("slo_report.xlsx")
	if err != nil {
		log.Panicf("Failed to save file: %v", err)
	}
}

func createStyle(f *excelize.File, font *excelize.Font, opts ...interface{}) int {
	style := &excelize.Style{Font: font}
	for _, opt := range opts {
		switch v := opt.(type) {
		case excelize.Alignment:
			style.Alignment = &v
		case excelize.Fill:
			style.Fill = v
		}
	}
	styleID, err := f.NewStyle(style)
	handleError(err, "Failed to create style")
	return styleID
}

func setSheetView(f *excelize.File) {
	handleError(f.SetSheetView("Sheet1", 0, &excelize.ViewOptions{
		ShowGridLines: &[]bool{true}[0],
		ZoomScale:     &[]float64{150}[0],
	}), "Failed to set sheet view")
	f.SetActiveSheet(0)
}

func setColWidth(f *excelize.File, sheet string, columns map[string]float64) {
	for rangeStr, width := range columns {
		// 範囲指定（例: "B-E"）を開始列と終了列に分割
		parts := strings.SplitN(rangeStr, "-", 2)
		startCol := parts[0]
		endCol := startCol
		if len(parts) > 1 {
			endCol = parts[1]
		}

		err := f.SetColWidth(sheet, startCol, endCol, width)
		handleError(err, "Failed to set column width")
	}
}

func setCellWithStyle(f *excelize.File, cell string, value interface{}, styleID int) {
	handleError(f.SetCellValue("Sheet1", cell, value), "Failed to set cell value")
	handleError(f.SetCellStyle("Sheet1", cell, cell, styleID), "Failed to set cell style")
}

func setCellValue(f *excelize.File, cell string, value interface{}) {
	handleError(f.SetCellValue("Sheet1", cell, value), "Failed to set cell value")
}

func listSLOs(ctx context.Context, client *monitoring.ServiceMonitoringClient) []*monitoringpb.ServiceLevelObjective {
	var slos []*monitoringpb.ServiceLevelObjective

	services := client.ListServices(ctx, &monitoringpb.ListServicesRequest{
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

		lSLOs := client.ListServiceLevelObjectives(ctx, &monitoringpb.ListServiceLevelObjectivesRequest{
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

			metrics, err := client.GetServiceLevelObjective(ctx, &monitoringpb.GetServiceLevelObjectiveRequest{
				Name: slo.GetName(),
			})
			if err != nil {
				log.Panicf("Failed to get service level objective: %v", err)
			}

			slos = append(slos, metrics)
		}
	}

	return slos
}

func processSLOs(ctx context.Context, client *monitoring.MetricClient, slos []*monitoringpb.ServiceLevelObjective) map[string]*sloData {
	var (
		data         = make(map[string]*sloData)
		warnMessages []string
	)

	bar := progressbar.Default(int64(len(slos)))
	var wg sync.WaitGroup

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
					result, err := processSLO(ctx, client, slo)
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
					err := bar.Finish()
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
	err := bar.Finish()
	if err != nil {
		log.Printf("Failed to finish progress bar: %v", err)
	}
	close(resultCh)
	close(errCh)

	return data
}

func handleError(err error, message string) {
	if err != nil {
		log.Fatalf("%s: %v", message, err)
	}
}
