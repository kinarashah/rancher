package client

const (
	AgentDeploymentCustomizationType                   = "agentDeploymentCustomization"
	AgentDeploymentCustomizationFieldAppendTolerations = "appendTolerations"
)

type AgentDeploymentCustomization struct {
	AppendTolerations []Toleration `json:"appendTolerations,omitempty" yaml:"appendTolerations,omitempty"`
}
