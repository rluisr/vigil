package model

// CloudProvider identifies a supported cloud provider.
type CloudProvider string

// Supported cloud providers.
const (
	CloudProviderGCP CloudProvider = "gcp"
	CloudProviderDD  CloudProvider = "datadog"
)
