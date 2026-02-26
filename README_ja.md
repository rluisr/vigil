![og](./assets/og.png)

**Vigil** はエラーバジェットの時系列データを分析し、活用されていない SLO を特定するツールです。検出結果を Excel レポートとして出力し、パフォーマンス傾向と最適化の推奨事項を提供します。

[English README](./README.md)

## 機能

- 指定期間内にエラーバジェットが設定閾値を一度も下回っていない SLO の検出
- ウィンドウ全体の 50% 以上でエラーバジェットが負の SLO の検出
- スタイル付き Excel レポート出力（`slo_report.xlsx`）
- マルチクラウド SLO モニタリング
  - Google Cloud Monitoring
  - Datadog
- レポート出力の多言語対応（英語 / 日本語）

![screenshot](./assets/excel.png)

## インストール

```bash
go install github.com/rluisr/vigil@main
```

## 認証

### GCP

Vigil は [Application Default Credentials (ADC)](https://cloud.google.com/docs/authentication/application-default-credentials) を使用します。事前に認証情報を設定してください：

```bash
gcloud auth application-default login
```

### Datadog

以下の環境変数を設定してください：

```bash
export DD_API_KEY="your-api-key"
export DD_APP_KEY="your-app-key"
```

## 使い方

### 引数

```
--cloud string
      クラウドプロバイダー: "gcp" または "datadog"（デフォルト "gcp"）
--gcp-project string
      GCP プロジェクト ID（GCP 使用時は必須）
--dd-site string
      Datadog サイト（例: datadoghq.com, ap1.datadoghq.com, datadoghq.eu）
--error-budget-threshold float
      エラーバジェットの閾値、0 〜 1（デフォルト 0.9）
--window duration
      対象ウィンドウ、"h" サフィックスを使用（デフォルト 720h0m0s）
--lang string
      レポート言語: "en" または "ja"（デフォルト "en"）
```

### 使用例

#### GCP: 30 日間エラーバジェットが 99% を下回っていない SLO を検出

```bash
vigil --cloud gcp --gcp-project your-gcp-project-id --error-budget-threshold 0.99 --window 720h
```

#### Datadog: 14 日間エラーバジェットが 95% を下回っていない SLO を検出

```bash
vigil --cloud datadog --dd-site datadoghq.com --error-budget-threshold 0.95 --window 336h
```

#### 日本語レポートを生成

```bash
vigil --cloud gcp --gcp-project your-gcp-project-id --lang ja
```

## ライセンス

WTFPL
