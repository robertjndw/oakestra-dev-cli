package oakapi

import (
	"encoding/json"
	"fmt"
)

// ObjectID is a document id that system_manager's endpoints sometimes render
// as a plain string and sometimes as MongoDB extended JSON ({"$oid": "..."})
// depending on the code path that produced the response. Fields typed
// ObjectID accept either shape, so callers don't have to special-case it
// themselves the way helpers.py's object_id() does at every call site.
type ObjectID string

func (o *ObjectID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*o = ObjectID(s)
		return nil
	}

	var extended struct {
		OID string `json:"$oid"`
	}
	if err := json.Unmarshal(data, &extended); err != nil {
		return fmt.Errorf("object id is neither a string nor {\"$oid\": ...}: %s", data)
	}
	*o = ObjectID(extended.OID)
	return nil
}

// Addresses is the optional per-microservice address request. It's a
// pointer with omitempty on the embedding field so it disappears from the
// payload entirely when unset - Oakestra treats a present-but-empty
// addresses object differently from a missing one.
type Addresses struct {
	RRIP string `json:"rr_ip,omitempty"`
}

// Microservice is a minimal container workload for an SLA document. Field
// order matters here since it's also the JSON encoding order, and the
// golden tests pin it to match tests/helpers.py's build_microservice
// exactly. Addresses has to stay last: it's the only field allowed to be
// absent.
//
// Nothing but Addresses gets `omitempty`. Oakestra's API expects every
// other field explicitly, zero values included (memory 0, empty strings,
// false), same as the Python dict literal this replaces.
type Microservice struct {
	MicroserviceID ObjectID   `json:"microserviceID"`
	Name           string     `json:"microservice_name"`
	Namespace      string     `json:"microservice_namespace"`
	Virtualization string     `json:"virtualization"`
	Cmd            []string   `json:"cmd"`
	Memory         int        `json:"memory"`
	VCPUs          int        `json:"vcpus"`
	VGPUs          int        `json:"vgpus"`
	VTPUs          int        `json:"vtpus"`
	BandwidthIn    int        `json:"bandwidth_in"`
	BandwidthOut   int        `json:"bandwidth_out"`
	Storage        int        `json:"storage"`
	Code           string     `json:"code"`
	State          string     `json:"state"`
	Port           string     `json:"port"`
	OneShot        bool       `json:"one_shot"`
	AddedFiles     []string   `json:"added_files"`
	Constraints    []string   `json:"constraints"`
	Addresses      *Addresses `json:"addresses,omitempty"`
}

// MicroserviceOption customizes a Microservice built by NewMicroservice.
type MicroserviceOption func(*Microservice)

// WithImage overrides the default nginx image.
func WithImage(image string) MicroserviceOption {
	return func(m *Microservice) { m.Code = image }
}

// WithCmd sets the container entrypoint command.
func WithCmd(cmd ...string) MicroserviceOption {
	return func(m *Microservice) { m.Cmd = cmd }
}

// WithMemory overrides the default 100 MB memory request.
func WithMemory(mb int) MicroserviceOption {
	return func(m *Microservice) { m.Memory = mb }
}

// WithAddresses requests a fixed round-robin service IP.
func WithAddresses(a Addresses) MicroserviceOption {
	return func(m *Microservice) { m.Addresses = &a }
}

// WithOneShot marks the microservice as one_shot: it runs to completion
// rather than staying up, and its exit code becomes the deployment verdict.
func WithOneShot() MicroserviceOption {
	return func(m *Microservice) { m.OneShot = true }
}

// NewMicroservice builds a minimal container microservice for an SLA
// document, matching tests/helpers.py's build_microservice defaults:
// nginx:latest, 100 MB memory, namespace "test", no cmd/addresses, not
// one_shot. Cmd/AddedFiles/Constraints start as non-nil empty slices since a
// nil Go slice marshals to `null` instead of `[]`, which Oakestra's SLA
// parser doesn't expect (Python's `cmd or []` always produced `[]`).
func NewMicroservice(name string, opts ...MicroserviceOption) Microservice {
	m := Microservice{
		Name:           name,
		Namespace:      "test",
		Virtualization: "container",
		Cmd:            []string{},
		Memory:         100,
		VCPUs:          1,
		Code:           "docker.io/library/nginx:latest",
		AddedFiles:     []string{},
		Constraints:    []string{},
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// Application is one application entry in an SLA document.
type Application struct {
	ApplicationID ObjectID       `json:"applicationID"`
	Name          string         `json:"application_name"`
	Namespace     string         `json:"application_namespace"`
	Desc          string         `json:"application_desc"`
	Microservices []Microservice `json:"microservices"`
}

// SLA is a minimal SLA document accepted by POST /api/application/.
type SLA struct {
	SLAVersion   string        `json:"sla_version"`
	CustomerID   string        `json:"customerID"`
	Applications []Application `json:"applications"`
}

// NewSLA builds a minimal SLA document for appName, matching
// tests/helpers.py's build_sla.
func NewSLA(appName string, ms ...Microservice) SLA {
	if ms == nil {
		ms = []Microservice{} // same null-vs-[] reasoning as NewMicroservice
	}
	return SLA{
		SLAVersion: "v2.0",
		CustomerID: "Admin",
		Applications: []Application{
			{
				Name:          appName,
				Namespace:     "test",
				Desc:          "oakestra-dev-cli E2E app",
				Microservices: ms,
			},
		},
	}
}
