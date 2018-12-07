package capabilities

import "net/http"

func NewNodeTemplateHandler() *NodeTemplateHandler {
	return &NodeTemplateHandler{}
}

type NodeTemplateHandler struct {
}


type nodeTemplateRequestBody struct {
	CredentialID string `json:"credentialId"`
	Action string `json:"action"`
}

func (h *NodeTemplateHandler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {

}