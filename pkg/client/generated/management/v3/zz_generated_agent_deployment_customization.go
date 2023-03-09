package client

const (
	AgentDeploymentCustomizationType                               = "agentDeploymentCustomization"
	AgentDeploymentCustomizationFieldAppendTolerations             = "appendTolerations"
	AgentDeploymentCustomizationFieldContainerResourceRequirements = "containerResourceRequirements"
	AgentDeploymentCustomizationFieldOverrideAffinities            = "overrideAffinities"
)

type AgentDeploymentCustomization struct {
	AppendTolerations             []Toleration          `json:"appendTolerations,omitempty" yaml:"appendTolerations,omitempty"`
	ContainerResourceRequirements *ResourceRequirements `json:"containerResourceRequirements,omitempty" yaml:"containerResourceRequirements,omitempty"`
	OverrideAffinities            []Affinity            `json:"overrideAffinities,omitempty" yaml:"overrideAffinities,omitempty"`
}
