//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"

	"oak-dev/internal/oakapi"
)

// webRRIP is a round-robin service IP from Oakestra's 10.30.0.0/16 service
// range, requested explicitly in the SLA so the client microservice's
// command knows the target upfront - matches tests/test_04_network.py.
const webRRIP = "10.30.30.30"

// TestOverlayNetwork proves the overlay data plane works end to end, ported
// from tests/test_04_network.py: an nginx 'web' service gets a fixed
// round-robin service IP, then a one-shot busybox 'client' wgets that IP
// from inside its own container. NodeEngine reports a one-shot job that
// exits 0 as COMPLETED, so a COMPLETED client is the entire proof that
// traffic actually crossed client netns -> proxy tun -> service IP
// translation -> web instance; a client that silently never ran would
// otherwise look exactly like a pass.
func TestOverlayNetwork(t *testing.T) {
	// No t.Parallel: web must reach RUNNING before the client is deployed,
	// and both share the one worker node with every other test in this
	// package.
	s := settings(t)
	cli := rootAPI(t)
	activeCluster(t)

	clientCmd := fmt.Sprintf(
		"for i in $(seq 20); do wget -q -O- -T 3 http://%s && exit 0; sleep 3; done; exit 1",
		webRRIP,
	)

	appName := fmt.Sprintf("e2enet%d", time.Now().Unix()%1_000_000)
	a := registerApp(t, cli, appName,
		oakapi.NewMicroservice("web", oakapi.WithAddresses(oakapi.Addresses{RRIP: webRRIP})),
		oakapi.NewMicroservice("client",
			oakapi.WithImage("docker.io/library/busybox:latest"),
			oakapi.WithCmd("sh", "-c", clientCmd),
			oakapi.WithOneShot(),
		),
	)
	webID := a.Services["web"]
	clientID := a.Services["client"]

	// Registered after registerApp's own t.Cleanup (which deletes the
	// application), so it runs first: cleanups run LIFO, and instances must
	// be undeployed before the application that owns them is deleted.
	t.Cleanup(func() {
		undeployAllInstances(t, cli, webID)
		undeployAllInstances(t, cli, clientID)
	})

	if !t.Run("web reaches RUNNING with its requested service IP", func(t *testing.T) {
		deployInstance(t, cli, webID)
		waitForRunningCount(t, cli, s, webID, 1)

		detail, err := serviceDetail(cli, webID)
		if err != nil {
			t.Fatalf("get service %s: %v", webID, err)
		}
		if detail == nil {
			t.Fatalf("web service %s disappeared after reaching RUNNING", webID)
		}
		if detail.RRIP != webRRIP {
			t.Fatalf("RR_ip = %q, want %q (requested service IP not set)", detail.RRIP, webRRIP)
		}
	}) {
		return
	}

	t.Run("client completes its overlay request to web", func(t *testing.T) {
		deployInstance(t, cli, clientID)

		waitFor(t, oakapi.Wait{
			Timeout:  s.DeployTimeout,
			Interval: 5 * time.Second,
			Desc:     fmt.Sprintf("client one-shot wget against %s to COMPLETE over the overlay", webRRIP),
		}, func() (struct{}, error) {
			job, err := getService(cli, clientID)
			if err != nil {
				return struct{}{}, err
			}
			if job == nil {
				return struct{}{}, oakapi.Terminal("client service %s disappeared while waiting", clientID)
			}
			// FAILED/DEAD here means the wget loop exhausted its retries -
			// the overlay did not deliver traffic to the web service.
			if err := job.NotFailed(); err != nil {
				return struct{}{}, err
			}
			if job.Status == "COMPLETED" {
				return struct{}{}, nil
			}
			for _, inst := range job.InstanceList {
				if inst.Status == "COMPLETED" {
					return struct{}{}, nil
				}
			}
			return struct{}{}, fmt.Errorf("not COMPLETED yet; job status %s", job.Status)
		})
	})
}
