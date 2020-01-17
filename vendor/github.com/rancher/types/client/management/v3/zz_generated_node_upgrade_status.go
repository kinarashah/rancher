package client

const (
	NodeUpgradeStatusType                  = "nodeUpgradeStatus"
	NodeUpgradeStatusFieldCurrentToken     = "currentToken"
	NodeUpgradeStatusFieldLastAppliedToken = "lastAppliedToken"
	NodeUpgradeStatusFieldNodes            = "nodes"
	NodeUpgradeStatusFieldState            = "state"
)

type NodeUpgradeStatus struct {
	CurrentToken     string                       `json:"currentToken,omitempty" yaml:"currentToken,omitempty"`
	LastAppliedToken string                       `json:"lastAppliedToken,omitempty" yaml:"lastAppliedToken,omitempty"`
	Nodes            map[string]map[string]string `json:"nodes,omitempty" yaml:"nodes,omitempty"`
	State            string                       `json:"state,omitempty" yaml:"state,omitempty"`
}
