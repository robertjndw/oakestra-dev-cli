package oakapi

import (
	"encoding/json"
	"testing"
)

const nginxGolden = `{"microserviceID":"","microservice_name":"nginx","microservice_namespace":"test","virtualization":"container","cmd":[],"memory":100,"vcpus":1,"vgpus":0,"vtpus":0,"bandwidth_in":0,"bandwidth_out":0,"storage":0,"code":"docker.io/library/nginx:latest","state":"","port":"","one_shot":false,"added_files":[],"constraints":[]}`

const webGolden = `{"microserviceID":"","microservice_name":"web","microservice_namespace":"test","virtualization":"container","cmd":[],"memory":100,"vcpus":1,"vgpus":0,"vtpus":0,"bandwidth_in":0,"bandwidth_out":0,"storage":0,"code":"docker.io/library/nginx:latest","state":"","port":"","one_shot":false,"added_files":[],"constraints":[],"addresses":{"rr_ip":"10.30.30.30"}}`

const clientGolden = `{"microserviceID":"","microservice_name":"client","microservice_namespace":"test","virtualization":"container","cmd":["sh","-c","for i in $(seq 20); do wget -q -O- -T 3 http://10.30.30.30 && exit 0; sleep 3; done; exit 1"],"memory":100,"vcpus":1,"vgpus":0,"vtpus":0,"bandwidth_in":0,"bandwidth_out":0,"storage":0,"code":"docker.io/library/busybox:latest","state":"","port":"","one_shot":true,"added_files":[],"constraints":[]}`

const slaGolden = `{"sla_version":"v2.0","customerID":"Admin","applications":[{"applicationID":"","application_name":"e2e123","application_namespace":"test","application_desc":"oakestra-dev-cli E2E app","microservices":[{"microserviceID":"","microservice_name":"nginx","microservice_namespace":"test","virtualization":"container","cmd":[],"memory":100,"vcpus":1,"vgpus":0,"vtpus":0,"bandwidth_in":0,"bandwidth_out":0,"storage":0,"code":"docker.io/library/nginx:latest","state":"","port":"","one_shot":false,"added_files":[],"constraints":[]}]}]}`

func marshal(t *testing.T, v any) string {
	t.Helper()
	raw, err := MarshalJSON(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(raw)
}

// A nil Go slice marshals to `null` instead of `[]`. Python's `cmd or []`
// always emitted `[]`, which is what Oakestra's SLA parser expects - watch
// for this regressing if NewMicroservice changes.
func TestMicroserviceMarshalsEveryFieldAtZero(t *testing.T) {
	got := marshal(t, NewMicroservice("nginx"))
	if got != nginxGolden {
		t.Errorf("marshal =\n  %s\nwant\n  %s", got, nginxGolden)
	}
}

// addresses must be present when requested, and must be the last field in
// the object (matches build_microservice's dict insertion order).
func TestMicroserviceWithAddresses(t *testing.T) {
	got := marshal(t, NewMicroservice("web", WithAddresses(Addresses{RRIP: "10.30.30.30"})))
	if got != webGolden {
		t.Errorf("marshal =\n  %s\nwant\n  %s", got, webGolden)
	}
}

// one_shot plus a populated cmd array is the exact shape test_04_network.py
// uses for its verdict-by-exit-code client.
func TestOneShotClientMicroservice(t *testing.T) {
	got := marshal(t, NewMicroservice("client",
		WithImage("docker.io/library/busybox:latest"),
		WithCmd("sh", "-c", "for i in $(seq 20); do wget -q -O- -T 3 http://10.30.30.30 && exit 0; sleep 3; done; exit 1"),
		WithOneShot(),
	))
	if got != clientGolden {
		t.Errorf("marshal =\n  %s\nwant\n  %s", got, clientGolden)
	}
}

// addresses must vanish entirely (not marshal as null or {}) when no
// microservice in the SLA requests one, same as at the Microservice level.
func TestSLAMarshalsToGolden(t *testing.T) {
	got := marshal(t, NewSLA("e2e123", NewMicroservice("nginx")))
	if got != slaGolden {
		t.Errorf("marshal =\n  %s\nwant\n  %s", got, slaGolden)
	}
}

func TestObjectIDAcceptsStringOrExtendedJSON(t *testing.T) {
	tests := []struct {
		name string
		json string
		want ObjectID
		fail bool
	}{
		{name: "plain string", json: `"abc123"`, want: "abc123"},
		{name: "extended JSON", json: `{"$oid":"abc123"}`, want: "abc123"},
		{name: "empty string", json: `""`, want: ""},
		{name: "neither shape", json: `42`, fail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var id ObjectID
			err := json.Unmarshal([]byte(tt.json), &id)
			if tt.fail {
				if err == nil {
					t.Fatalf("Unmarshal(%s) = nil error, want one", tt.json)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal(%s): %v", tt.json, err)
			}
			if id != tt.want {
				t.Errorf("Unmarshal(%s) = %q, want %q", tt.json, id, tt.want)
			}
		})
	}
}
