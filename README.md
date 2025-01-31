Vigil
=====

Multi-cloud SLO guardian that illuminates your error budget health across monitoring platforms.

![screenshot](./assets/excel.png)

## Features
- Get a list of SLOs to be adjusted that have never been below n% in m days
- Excel report generation
- Multi-cloud SLO monitoring (currently supports Google Cloud Monitoring)

## Arguments
```
  -error-budget-threshold float
        error budget threshold. 0 ~ 1 (default 0.9)
  -project string
        project id
  -window duration
        target window. use "h" suffix (default 720h0m0s)
```

### Get a list of SLOs to be adjusted that have never been below 99% in 30 days
```bash
$ vigil --project your-gcp-project-id --error-budget-threshold 0.99 --window 720h
```

## Install
```bash
$ go install github.com/rluisr/vigil@main
```

## Coming Soon
- Datadog integration
