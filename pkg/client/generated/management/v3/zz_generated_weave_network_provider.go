package client

const (
	WeaveNetworkProviderType          = "weaveNetworkProvider"
	WeaveNetworkProviderFieldKinara   = "kinara"
	WeaveNetworkProviderFieldPassword = "password"
)

type WeaveNetworkProvider struct {
	Kinara   string `json:"kinara,omitempty" yaml:"kinara,omitempty"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`
}
