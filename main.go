package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/xuri/excelize/v2"

	"github.com/rluisr/vigil/gcp"
	"github.com/rluisr/vigil/model"
	"github.com/rluisr/vigil/utils"
)

const maxConcurrency = 16

var (
	cloudProvider        = flag.String("cloud", string(model.CloudProviderGCP), "cloud provider. gcp or datadog(WIP)")
	gcpProjectID         = flag.String("gcp-project", "", "project id")
	errorBudgetThreshold = flag.Float64("error-budget-threshold", 0.9, "error budget threshold. 0 ~ 1") // Error budget threshold
	window               = flag.Duration("window", 720*time.Hour, "target window. use \"h\" suffix")
	warnMessages         = []string{}
	warnMutex            sync.Mutex
)

func main() {
	flag.Parse()
	validateFlags()

	ctx := context.Background()

	client, err := gcp.NewClient(ctx, *gcpProjectID, *errorBudgetThreshold, *window)
	if err != nil {
		log.Panicf("Failed to create client: %v", err)
	}
	defer client.MonitoringClient.Close()
	defer client.MetricClient.Close()

	var vigil Vigil = client

	log.Println("Getting SLOs...")

	slos, err := vigil.GetSLOs(ctx)
	if err != nil {
		log.Panicf("Failed to list SLOs: %v", err)
	}

	bar := progressbar.Default(int64(len(slos)))

	var sloData = make(map[string]*model.SLOData)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrency)
	errChan := make(chan error, len(slos))

	for _, slo := range slos {
		wg.Add(1)

		sem <- struct{}{}

		go func(s *model.SLO) {
			defer wg.Done()
			defer func() { <-sem }()

			data, err := processSLO(ctx, vigil, s)
			if err != nil {
				errChan <- fmt.Errorf("failed to process SLO %s: %w", s.DisplayName, err)
				return
			}
			if data != nil {
				mu.Lock()
				for k, v := range data {
					sloData[k] = v
				}
				err := bar.Add(1)
				if err != nil {
					log.Printf("Failed to update progress bar: %v", err)
				}
				mu.Unlock()
			}
		}(slo)
	}

	wg.Wait()
	close(errChan)
	err = bar.Finish()
	if err != nil {
		log.Printf("Failed to finish progress bar: %v", err)
	}

	if len(errChan) > 0 {
		err = <-errChan
		log.Panicf("Error in processing SLOs: %v", err)
	}

	generateExcelReport(sloData)

	for _, msg := range warnMessages {
		log.Println(msg)
	}

	log.Println("Report has been written to slo_report.xlsx")
}

func processSLO(ctx context.Context, client Vigil, slo *model.SLO) (map[string]*model.SLOData, error) {
	var (
		data = make(map[string]*model.SLOData)
	)

	goodQuery, totalQuery, points, err := client.GetErrorBudgetTimeSeries(ctx, slo)
	if err != nil {
		if strings.Contains(err.Error(), "no data points found") {
			warnMutex.Lock()
			warnMessages = append(warnMessages, err.Error())
			warnMutex.Unlock()
			return nil, nil
		}
		return nil, err
	}

	var flagBelowThreshold bool // The error budget has never been below n% for m days
	var flagNegative bool       // Error budget is a negative throughout the window
	for _, point := range points {
		if point >= *errorBudgetThreshold {
			flagBelowThreshold = true
			break
		}
	}
	flagNegative = utils.IsPercentNegative(points, 0.5)

	minBudget, avgBudget := utils.GetMinAvgErrorBudget(points)

	data[slo.DisplayName] = &model.SLOData{
		Flag:       flagBelowThreshold || flagNegative,
		SLO:        slo.Goal,
		GoodQuery:  goodQuery,
		TotalQuery: totalQuery,
		AvgBudget:  avgBudget,
		MinBudget:  minBudget,
	}

	return data, nil
}

func validateFlags() {
	if *gcpProjectID == "" {
		log.Panicf("--gcp-project id is required")
	}
	if *errorBudgetThreshold <= 0 || *errorBudgetThreshold >= 1 {
		log.Panicf("--error-budget-threshold must be between 0 and 1")
	}
	if *window <= 0 {
		log.Panicf("--window must be positive duration")
	}

	if *cloudProvider != "gcp" {
		log.Panicf("not supported cloud provider yet: %s", *cloudProvider)
	}
}

func generateExcelReport(data map[string]*model.SLOData) {
	f := excelize.NewFile()
	defer func() {
		err := f.Close()
		if err != nil {
			log.Printf("Failed to close file: %v", err)
		}
	}()

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
	setCellWithStyle(f, "A1", fmt.Sprintf("SLO Report for %s\nList of SLOs that have never been below %g%% in %g days and 50%% of the total window has a negative error budget",
		*gcpProjectID, *errorBudgetThreshold*100, window.Hours()/24), descriptionStyle)
	setCellWithStyle(f, "C2", "New SLO", highlightStyle)

	headers := []string{"Name", "SLO", "New SLO", "SLI Min", "SLI Avg", "GoodQuery", "TotalQuery", "New GoodQuery?", "New TotalQuery?"}
	for i, h := range headers {
		setCellWithStyle(f, fmt.Sprintf("%c2", 'A'+i), h, boldStyle)
	}

	row := 3
	for k, v := range data {
		if v.Flag {
			setCellValue(f, fmt.Sprintf("A%d", row), k)
			setCellValue(f, fmt.Sprintf("B%d", row), v.SLO*100)
			setCellWithStyle(f, fmt.Sprintf("C%d", row), 0, highlightStyle)
			setCellValue(f, fmt.Sprintf("D%d", row), v.MinBudget*100)
			setCellValue(f, fmt.Sprintf("E%d", row), v.AvgBudget*100)
			setCellValue(f, fmt.Sprintf("F%d", row), v.GoodQuery)
			setCellValue(f, fmt.Sprintf("G%d", row), v.TotalQuery)
			row++
		}
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
		// split range e.g B-E
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

func handleError(err error, message string) {
	if err != nil {
		log.Fatalf("%s: %v", message, err)
	}
}
